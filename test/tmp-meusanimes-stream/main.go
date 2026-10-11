package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/alvarorichard/Goanime/internal/api/providers"
	"github.com/alvarorichard/Goanime/internal/api/source"
)

func main() {
	ctx := context.Background()

	results, err := providers.SearchAll(
		ctx,
		"Gotoubun no Hanayome Dublado",
		source.MeusAnimes,
	)
	if err != nil {
		fmt.Println("ERRO NA BUSCA:", err)
		return
	}

	var animeURL string
	for _, anime := range results {
		if strings.Contains(anime.URL, "/a/gotoubun-no-hanayome-dublado/") {
			animeURL = anime.URL
			fmt.Println("Anime:", anime.Name)
			fmt.Println("Página:", anime.URL)
			break
		}
	}
	if animeURL == "" {
		fmt.Println("Anime não encontrado.")
		return
	}

	var animeSelected = results[0]
	for _, anime := range results {
		if anime.URL == animeURL {
			animeSelected = anime
			break
		}
	}

	episodes, err := providers.FetchEpisodes(ctx, animeSelected)
	if err != nil {
		fmt.Println("ERRO NOS EPISÓDIOS:", err)
		return
	}
	if len(episodes) == 0 {
		fmt.Println("Nenhum episódio encontrado.")
		return
	}

	episode := episodes[0]
	fmt.Println("Episódio:", episode.Number, episode.URL)
	fmt.Println("Resolvendo player...")

	streamURL, err := providers.FetchStreamURL(
		ctx, &episode, animeSelected, "best",
	)
	if err != nil {
		fmt.Println("ERRO NO PLAYER:", err)
		return
	}

	fmt.Println("URL DE VÍDEO:", streamURL)
}
