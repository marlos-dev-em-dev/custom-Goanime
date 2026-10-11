package main

import (
    "context"
    "fmt"

    "github.com/alvarorichard/Goanime/internal/api/providers"
)

func main() {
    results, err := providers.SearchAll(context.Background(), "Black Clover")
    if err != nil {
        fmt.Println("ERRO:", err)
    }

    fmt.Printf("Resultados totais: %d\n", len(results))

    for _, anime := range results {
        fmt.Printf("- %s | %s\n", anime.Name, anime.URL)
    }
}
