package main

import (
	"errors"
	"strings"
)

func productValid(p Product) error {
	if strings.TrimSpace(p.Slug) == "" {
		return errors.New("slug obrigatório")
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name obrigatório")
	}
	if p.PriceCents <= 0 {
		return errors.New("priceCents deve ser > 0")
	}
	return nil
}
