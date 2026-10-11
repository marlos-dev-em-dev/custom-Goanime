package animesonline

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/alvarorichard/Goanime/internal/models"
	"github.com/alvarorichard/Goanime/internal/scraper/netx"
	"github.com/alvarorichard/Goanime/internal/util"
)

const BaseURL = "https://animesonlinecc.to"

var episodeRe = regexp.MustCompile(`(?i)(?:epis[oó]dio-?|episode-?)(\d+)`)

type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{
		http: util.NewFastClient(),
	}
}

func (c *Client) get(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("animesonline: criando request: %w", err)
	}

	req.Header.Set("User-Agent", netx.UserAgent)
	req.Header.Set(
		"Accept",
		"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
	)
	req.Header.Set("Accept-Language", "pt-BR,pt;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Referer", BaseURL+"/")

	resp, err := c.http.Do(req) // #nosec G704
	if err != nil {
		return nil, fmt.Errorf("animesonline: request: %w", err)
	}

	if err := netx.CheckHTTPStatus(resp, "AnimesOnline"); err != nil {
		_ = resp.Body.Close()
		return nil, err
	}

	return resp, nil
}

func (c *Client) Search(ctx context.Context, query string) ([]*models.Anime, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []*models.Anime{}, nil
	}
	query = strings.ReplaceAll(strings.TrimSpace(query), "-", " ")

	u, err := url.Parse(BaseURL + "/")
	if err != nil {
		return nil, fmt.Errorf("animesonline: parse search url: %w", err)
	}

	q := u.Query()
	q.Set("s", query)
	u.RawQuery = q.Encode()

	resp, err := c.get(ctx, u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("animesonline: parse search html: %w", err)
	}

	results := make([]*models.Anime, 0)

	doc.Find("article.item").Each(func(_ int, article *goquery.Selection) {
		link := article.Find("a[href*='/anime/']").First()
		href, ok := link.Attr("href")
		if !ok || strings.TrimSpace(href) == "" {
			return
		}

		image := article.Find("img").First()
		title, _ := image.Attr("alt")

		if strings.TrimSpace(title) == "" {
			title = article.Find(".data h3").First().Text()
		}

		title = strings.TrimSpace(title)
		if title == "" {
			return
		}

		imageURL, _ := image.Attr("src")

		results = append(results, &models.Anime{
			Name:      title,
			URL:       resolveURL(href),
			ImageURL:  resolveURL(imageURL),
			MediaType: models.MediaTypeAnime,
		})
	})

	return dedupeAnime(results), nil
}

func (c *Client) Episodes(ctx context.Context, animeURL string) ([]models.Episode, error) {
	resp, err := c.get(ctx, animeURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("animesonline: parse anime html: %w", err)
	}

	episodes := make([]models.Episode, 0)
	seen := make(map[string]struct{})

	doc.Find("a[href*='/episodio/']").Each(func(_ int, a *goquery.Selection) {
		href, ok := a.Attr("href")
		if !ok {
			return
		}

		href = resolveURL(href)
		if href == "" {
			return
		}

		if _, exists := seen[href]; exists {
			return
		}
		seen[href] = struct{}{}

		text := strings.TrimSpace(a.Text())
		number := parseEpisodeNumber(href, text)

		episodes = append(episodes, models.Episode{
			Number: number,
			Num:    atoi(number),
			URL:    href,
			Title: models.TitleDetails{
				Romaji: text,
			},
		})
	})

	// Preserva a ordem das temporadas no HTML quando não existe número válido.
	sort.SliceStable(episodes, func(i, j int) bool {
		if episodes[i].Num == 0 || episodes[j].Num == 0 {
			return false
		}
		return episodes[i].Num < episodes[j].Num
	})

	return episodes, nil
}

func (c *Client) Stream(ctx context.Context, episodeURL string) (string, error) {
	resp, err := c.get(ctx, episodeURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("animesonline: parse episode html: %w", err)
	}

	// O option-1 é o primeiro servidor publicado pela página.
	// O player do GoAnime já sabe resolver blogger.com/video.g?token=...
	selectors := []string{
		"#option-1 iframe[src*='blogger.com/video']",
		"iframe[src*='blogger.com/video']",
		"iframe[src*='blogspot.com/video']",
	}

	for _, selector := range selectors {
		var result string

		doc.Find(selector).EachWithBreak(func(_ int, iframe *goquery.Selection) bool {
			src, ok := iframe.Attr("src")
			if !ok {
				return true
			}

			src = strings.TrimSpace(src)
			if src == "" {
				return true
			}

			result = src
			return false
		})

		if result != "" {
			return result, nil
		}
	}

	return "", fmt.Errorf("animesonline: nenhum player Blogger encontrado")
}

func resolveURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	base, err := url.Parse(BaseURL)
	if err != nil {
		return raw
	}

	return base.ResolveReference(u).String()
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
		if anime == nil || anime.URL == "" {
			continue
		}

		if _, exists := seen[anime.URL]; exists {
			continue
		}

		seen[anime.URL] = struct{}{}
		out = append(out, anime)
	}

	return out
}
