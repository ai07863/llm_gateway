package main

import (
	"log"

	"llm-gateway/internal/api"
	"llm-gateway/internal/config"
)

func main() {
	s, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	r := api.NewRouter(s)
	log.Printf("listening on %s", s.ListenAddr)
	if err := r.Run(s.ListenAddr); err != nil {
		log.Fatal(err)
	}
}