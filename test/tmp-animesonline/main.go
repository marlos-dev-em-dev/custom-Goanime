package main

import (
    "context"
    "fmt"

    "github.com/alvarorichard/Goanime/internal/scraper/providers/animesonline"
)

func main() {
    client := animesonline.NewClient()

    results, err := client.Search(context.Background(), "Black Clover")
    if err != nil {
        panic(err)
    }

    fmt.Printf("Resultados: %d\n", len(results))

    for _, anime := range results {
        fmt.Printf("- %s | %s\n", anime.Name, anime.URL)
    }
}
