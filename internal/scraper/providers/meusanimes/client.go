package meusanimes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/alvarorichard/Goanime/internal/scraper/providers/superflix"

	"github.com/alvarorichard/Goanime/internal/models"
	"github.com/alvarorichard/Goanime/internal/scraper/netx"
	"github.com/alvarorichard/Goanime/internal/util"
)

const BaseURL = "https://meusanimes.blog"

var (
	episodeRe = regexp.MustCompile(
		`(?i)(?:epis[oó]dio|episode|ep)[^0-9]{0,16}(\d+)`,
	)

	bloggerURLRe = regexp.MustCompile(
		`(?i)https?://(?:www\.)?blogger\.com/video\.g\?token=[A-Za-z0-9_=-]+`,
	)

	mediaURLRe = regexp.MustCompile(
		`(?i)https?://[^\s"'<>\\]+?\.(?:m3u8|mp4)(?:\?[^\s"'<>\\]*)?`,
	)
)

type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{
		http: util.NewFastClient(),
	}
}

func (c *Client) getBody(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		rawURL,
		http.NoBody,
	)
	if err != nil {
		return nil, fmt.Errorf("meusanimes: criando request: %w", err)
	}

	req.Header.Set("User-Agent", netx.UserAgent)
	req.Header.Set(
		"Accept",
		"text/html,application/json,application/xhtml+xml,*/*;q=0.8",
	)
	req.Header.Set(
		"Accept-Language",
		"pt-BR,pt;q=0.9,en-US;q=0.8,en;q=0.7",
	)
	req.Header.Set("Referer", BaseURL+"/")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("meusanimes: request: %w", err)
	}

	if resp.StatusCode == http.StatusForbidden ||
		resp.StatusCode == http.StatusServiceUnavailable {
		_ = resp.Body.Close()

		util.Debug(
			"MeusAnimes: HTTP blocked; retrying with browser",
			"url", rawURL,
			"status", resp.StatusCode,
		)

		body, browserErr := superflix.FetchHTMLWithBrowser(
			ctx,
			rawURL,
			60*time.Second,
		)
		if browserErr != nil {
			return nil, fmt.Errorf(
				"meusanimes: HTTP %d and browser fallback failed: %w",
				resp.StatusCode,
				browserErr,
			)
		}

		return body, nil
	}

	if err := netx.CheckHTTPStatus(resp, "MeusAnimes"); err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return nil, fmt.Errorf("meusanimes: lendo resposta: %w", err)
	}

	return body, nil
}

// Search tenta primeiro a API do site e depois a busca HTML.
func (c *Client) Search(
	ctx context.Context,
	query string,
) ([]*models.Anime, error) {
	query = strings.TrimSpace(
		strings.ReplaceAll(strings.TrimSpace(query), "-", " "),
	)

	if query == "" {
		return []*models.Anime{}, nil
	}

	searchURL, err := url.Parse(BaseURL + "/")
	if err != nil {
		return nil, fmt.Errorf("meusanimes: parse search URL: %w", err)
	}

	params := searchURL.Query()
	params.Set("s", query)
	searchURL.RawQuery = params.Encode()

	body, err := c.getBody(ctx, searchURL.String())
	if err != nil {
		return nil, err
	}

	results, err := parseSearchHTML(body)
	if err != nil {
		return nil, err
	}

	results = filterRelevantResults(results, query)

	return dedupeAnime(results), nil
}

func parseSearchJSON(body []byte) ([]*models.Anime, error) {
	var payload struct {
		Data []struct {
			Name      string `json:"name"`
			Slug      string `json:"slug"`
			Poster    string `json:"poster"`
			URL       string `json:"url"`
			Thumbnail string `json:"thumbnail_url"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	results := make([]*models.Anime, 0, len(payload.Data))

	for _, item := range payload.Data {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}

		pageURL := animePageURL(item.URL, item.Slug)
		if pageURL == "" {
			continue
		}

		image := item.Poster
		if image == "" {
			image = item.Thumbnail
		}

		results = append(results, &models.Anime{
			Name:      name,
			URL:       pageURL,
			ImageURL:  resolvePoster(image),
			MediaType: models.MediaTypeAnime,
		})
	}

	return results, nil
}

func parseSearchHTML(body []byte) ([]*models.Anime, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("meusanimes: parse search HTML: %w", err)
	}

	results := make([]*models.Anime, 0)

	doc.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
		href, ok := a.Attr("href")
		if !ok {
			return
		}

		pageURL := resolveURL(href)
		if !isSitePath(pageURL, "/a/") {
			return
		}

		image := a.Find("img").First()

		title := strings.TrimSpace(
			a.Find("h2, h3, .title").First().Text(),
		)

		if title == "" {
			title, _ = image.Attr("alt")
			title = strings.TrimSpace(title)
		}

		if title == "" {
			title = strings.TrimSpace(a.Text())
		}

		if title == "" {
			return
		}

		imageURL := ""
		for _, attr := range []string{"data-src", "data-lazy-src", "src"} {
			if value, exists := image.Attr(attr); exists && value != "" {
				imageURL = resolveURL(value)
				break
			}
		}

		results = append(results, &models.Anime{
			Name:      title,
			URL:       pageURL,
			ImageURL:  imageURL,
			MediaType: models.MediaTypeAnime,
		})
	})

	return results, nil
}

func (c *Client) Episodes(
	ctx context.Context,
	animeURL string,
) ([]models.Episode, error) {
	body, err := c.getBody(ctx, animeURL)
	if err != nil {
		return nil, err
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("meusanimes: parse anime HTML: %w", err)
	}

	episodes := make([]models.Episode, 0)
	seen := make(map[string]struct{})

	doc.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
		href, ok := a.Attr("href")
		if !ok {
			return
		}

		episodeURL := resolveURL(href)
		if !isSitePath(episodeURL, "/e/") {
			return
		}

		if _, exists := seen[episodeURL]; exists {
			return
		}
		seen[episodeURL] = struct{}{}

		title := strings.TrimSpace(a.Text())
		number := parseEpisodeNumber(episodeURL, title)

		episodes = append(episodes, models.Episode{
			Number: number,
			Num:    atoi(number),
			URL:    episodeURL,
			Title: models.TitleDetails{
				Romaji: title,
			},
		})
	})

	sort.SliceStable(episodes, func(i, j int) bool {
		if episodes[i].Num == 0 || episodes[j].Num == 0 {
			return false
		}
		return episodes[i].Num < episodes[j].Num
	})

	return episodes, nil
}

// Stream procura primeiro os players legendado/dublado e depois URLs
// Blogger, MP4 ou HLS expostas no HTML.
func (c *Client) Stream(
	ctx context.Context,
	episodeURL string,
) (string, error) {
	body, err := c.getBody(ctx, episodeURL)
	if err != nil {
		return "", err
	}

	rawHTML := normalizeEmbedded(string(body))

	for _, key := range []string{"player_leg", "player_dub"} {
		value := findPlayerValue(rawHTML, key)
		if streamURL := extractPlayableURL(value); streamURL != "" {
			return streamURL, nil
		}
	}

	if streamURL := extractPlayableURL(rawHTML); streamURL != "" {
		return streamURL, nil
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("meusanimes: parse episode HTML: %w", err)
	}

	var externalPlayer string

	doc.Find("iframe[src]").EachWithBreak(
		func(_ int, iframe *goquery.Selection) bool {
			src, ok := iframe.Attr("src")
			if !ok {
				return true
			}

			src = resolveURL(src)

			if streamURL := extractPlayableURL(src); streamURL != "" {
				externalPlayer = streamURL
				return false
			}

			if strings.Contains(
				strings.ToLower(src),
				"serv01.meusdoramas.club",
			) {
				externalPlayer = src
			}

			return true
		},
	)

	if externalPlayer != "" {
		return c.resolveExternalPlayer(ctx, externalPlayer)
	}

	return "", fmt.Errorf(
		"meusanimes: nenhum link Blogger, MP4 ou HLS encontrado no episódio",
	)
}
func (c *Client) resolveExternalPlayer(
	ctx context.Context,
	playerURL string,
) (string, error) {
	player, err := url.Parse(playerURL)
	if err != nil {
		return "", fmt.Errorf("meusanimes: URL do player inválida: %w", err)
	}

	if !strings.EqualFold(player.Hostname(), "serv01.meusdoramas.club") {
		return "", fmt.Errorf("meusanimes: host de player não suportado: %s", player.Hostname())
	}

	// A URL do player usa o formato #/video/{tmdb}/{temporada}/{episódio}.
	parts := strings.Split(strings.Trim(player.Fragment, "/"), "/")
	if len(parts) != 4 || parts[0] != "video" {
		return "", fmt.Errorf("meusanimes: formato inesperado do player: %s", playerURL)
	}

	apiURL, err := url.Parse("https://serv01.meusdoramas.club/posts/get-video.php")
	if err != nil {
		return "", err
	}

	query := apiURL.Query()
	query.Set("tmdb", parts[1])
	query.Set("season_number", parts[2])
	query.Set("episode_number", parts[3])
	apiURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		apiURL.String(),
		http.NoBody,
	)
	if err != nil {
		return "", fmt.Errorf("meusanimes: criando requisição de vídeo: %w", err)
	}

	req.Header.Set("User-Agent", netx.UserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", "https://serv01.meusdoramas.club/")
	req.Header.Set("Origin", "https://serv01.meusdoramas.club")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("meusanimes: consultando API de vídeo: %w", err)
	}

	if err := netx.CheckHTTPStatus(resp, "MeusAnimes video API"); err != nil {
		_ = resp.Body.Close()
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Success  bool   `json:"success"`
		VideoURL string `json:"videoUrl"`
		Message  string `json:"message"`
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return "", fmt.Errorf("meusanimes: resposta inválida da API de vídeo: %w", err)
	}

	if !result.Success || strings.TrimSpace(result.VideoURL) == "" {
		return "", fmt.Errorf("meusanimes: API não forneceu vídeo: %s", result.Message)
	}

	videoURL := strings.TrimSpace(result.VideoURL)

	if playable := extractPlayableURL(videoURL); playable != "" {
		return playable, nil
	}

	return "", fmt.Errorf("meusanimes: formato de vídeo não reconhecido: %s", videoURL)
}
func findPlayerValue(rawHTML, key string) string {
	pattern := regexp.MustCompile(
		`(?is)` + regexp.QuoteMeta(key) +
			`["']?\s*:\s*["']([^"']+)`,
	)

	match := pattern.FindStringSubmatch(rawHTML)
	if len(match) < 2 {
		return ""
	}

	return match[1]
}

func extractPlayableURL(value string) string {
	value = normalizeEmbedded(value)

	if match := bloggerURLRe.FindString(value); match != "" {
		return strings.TrimRight(match, ".,;)")
	}

	if match := mediaURLRe.FindString(value); match != "" {
		return strings.TrimRight(match, ".,;)")
	}

	return ""
}

func normalizeEmbedded(value string) string {
	value = html.UnescapeString(value)
	value = strings.ReplaceAll(value, `\/`, `/`)
	value = strings.ReplaceAll(value, `\u0026`, "&")
	value = strings.ReplaceAll(value, `\u003d`, "=")
	value = strings.ReplaceAll(value, `\"`, `"`)

	return value
}

func animePageURL(rawURL, slug string) string {
	if rawURL != "" {
		pageURL := resolveURL(rawURL)
		parsed, err := url.Parse(pageURL)
		if err == nil {
			if strings.HasPrefix(parsed.Path, "/anime/") {
				slug = strings.Trim(
					strings.TrimPrefix(parsed.Path, "/anime/"),
					"/",
				)
				return resolveURL("/a/" + slug + "/")
			}
			return pageURL
		}
	}

	slug = strings.Trim(slug, "/")
	if slug == "" {
		return ""
	}

	return resolveURL("/a/" + slug + "/")
}

func resolvePoster(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err == nil && parsed.IsAbs() {
		return raw
	}

	if strings.HasPrefix(raw, "/") &&
		!strings.HasPrefix(raw, "/wp-content/") &&
		!strings.HasPrefix(raw, "/assets/") {
		return "https://image.tmdb.org/t/p/w500" + raw
	}

	return resolveURL(raw)
}

func resolveURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	base, err := url.Parse(BaseURL)
	if err != nil {
		return raw
	}

	return base.ResolveReference(parsed).String()
}

func isSitePath(rawURL, prefix string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	if !strings.EqualFold(parsed.Hostname(), "meusanimes.blog") {
		return false
	}

	path := strings.Trim(parsed.Path, "/")
	prefixPath := strings.Trim(prefix, "/")

	if !strings.HasPrefix(path, prefixPath+"/") {
		return false
	}

	// Exige um slug real depois de /a/ ou /e/.
	slug := strings.Trim(
		strings.TrimPrefix(path, prefixPath+"/"),
		"/",
	)

	return slug != ""
}

func parseEpisodeNumber(href, text string) string {
	if match := episodeRe.FindStringSubmatch(href); len(match) >= 2 {
		return match[1]
	}

	if match := episodeRe.FindStringSubmatch(text); len(match) >= 2 {
		return match[1]
	}

	return ""
}

func atoi(value string) int {
	n, _ := strconv.Atoi(value)
	return n
}

func dedupeAnime(results []*models.Anime) []*models.Anime {
	seen := make(map[string]struct{}, len(results))
	out := make([]*models.Anime, 0, len(results))

	for _, anime := range results {
		if anime == nil {
			continue
		}

		key := strings.ToLower(strings.TrimSpace(anime.URL))
		if key == "" {
			key = strings.ToLower(strings.TrimSpace(anime.Name))
		}

		if _, exists := seen[key]; exists {
			continue
		}

		seen[key] = struct{}{}
		out = append(out, anime)
	}

	return out
}

var searchTokenRe = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func filterRelevantResults(
	results []*models.Anime,
	query string,
) []*models.Anime {
	tokenize := func(value string) map[string]bool {
		value = strings.ToLower(value)
		value = searchTokenRe.ReplaceAllString(value, " ")

		words := strings.Fields(value)
		tokens := make(map[string]bool, len(words))

		for _, word := range words {
			if len([]rune(word)) > 1 {
				tokens[word] = true
			}
		}

		return tokens
	}

	queryTokens := tokenize(query)

	// Remove palavras comuns que não ajudam a identificar o título.
	for _, word := range []string{
		"no", "na", "nas", "nos", "de", "do", "da",
		"dos", "das", "the", "of", "and",
	} {
		delete(queryTokens, word)
	}

	if len(queryTokens) == 0 {
		return results
	}

	// Exige que pelo menos metade das palavras relevantes apareça no título.
	minimumMatches := (len(queryTokens) + 1) / 2

	filtered := make([]*models.Anime, 0, len(results))

	for _, anime := range results {
		if anime == nil {
			continue
		}

		titleTokens := tokenize(anime.Name)
		matches := 0

		for word := range queryTokens {
			if titleTokens[word] {
				matches++
			}
		}

		if matches >= minimumMatches {
			filtered = append(filtered, anime)
		}
	}

	return filtered
}
