package main

import (
	"context"
	"fmt"

	"github.com/alvarorichard/Goanime/internal/api/providers"
	"github.com/alvarorichard/Goanime/internal/api/source"
)

func main() {
	results, err := providers.SearchAll(
		context.Background(),
		"Gotoubun no Hanayome Dublado",
		source.MeusAnimes,
	)

	fmt.Printf("Erro: %v\n", err)
	fmt.Printf("Resultados: %d\n", len(results))

	for _, anime := range results {
		fmt.Printf("- %s | %s | %s\n",
			anime.Name, anime.Source, anime.URL)
	}
}
