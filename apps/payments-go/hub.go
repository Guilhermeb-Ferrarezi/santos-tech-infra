package main

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

func chargeChannel(token string) string { return fmt.Sprintf("pay:charge:%s", token) }

// publishChargePaid avisa os streams SSE inscritos no token que a cobrança foi paga.
func (s *Server) publishChargePaid(ctx context.Context, token string) {
	if token == "" {
		return
	}
	_ = s.rdb.Publish(ctx, chargeChannel(token), "paid").Err()
}

// publishChargeCanceled avisa os streams SSE inscritos que a cobrança foi cancelada.
func (s *Server) publishChargeCanceled(ctx context.Context, token string) {
	if token == "" {
		return
	}
	_ = s.rdb.Publish(ctx, chargeChannel(token), "canceled").Err()
}

// subscribeCharge devolve um pubsub do Redis para o token (lembre de Close no fim).
func (s *Server) subscribeCharge(ctx context.Context, token string) *redis.PubSub {
	return s.rdb.Subscribe(ctx, chargeChannel(token))
}

func recChannel(token string) string { return fmt.Sprintf("pay:rec:%s", token) }

// publishRecurrenceStatus avisa os streams SSE inscritos na assinatura (token público) que
// o status mudou (active/rejected/canceled/expired) — usado pela tela de checkout.
func (s *Server) publishRecurrenceStatus(ctx context.Context, token, status string) {
	if token == "" {
		return
	}
	_ = s.rdb.Publish(ctx, recChannel(token), status).Err()
}

// subscribeRecurrence devolve um pubsub do Redis para a assinatura (lembre de Close no fim).
func (s *Server) subscribeRecurrence(ctx context.Context, token string) *redis.PubSub {
	return s.rdb.Subscribe(ctx, recChannel(token))
}
