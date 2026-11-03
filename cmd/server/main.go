package main

import (
	"log"

	"llm-gateway/internal/api"
	"llm-gateway/internal/config"
	"llm-gateway/internal/pricing"
)

func main() {
	s, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	pricing.StartAutoRefresh(s.PricingURL, s.PricingRefreshInterval)
	r := api.NewRouter(s)
	log.Printf("listening on %s", s.ListenAddr)
	if err := r.Run(s.ListenAddr); err != nil {
		log.Fatal(err)
	}
}
