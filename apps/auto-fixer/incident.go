package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

type Incident struct {
	ID             string `json:"id"`
	App            string `json:"app"`
	Repo           string `json:"repo"`
	Branch         string `json:"branch"`
	Commit         string `json:"commit"`
	FixSHA         string `json:"fix_sha"`
	AnchorMsgID    string `json:"anchor_msg_id"`
	Status         string `json:"status"`
	Attempts       int    `json:"attempts"`
	DeploymentUUID string `json:"deployment_uuid"`
}

type Store struct{ rdb *redis.Client }

func (s *Store) Save(ctx context.Context, in *Incident) error {
	b, _ := json.Marshal(in)
	return s.rdb.Set(ctx, "incident:"+in.ID, b, 7*24*time.Hour).Err()
}

func (s *Store) Load(ctx context.Context, id string) (*Incident, error) {
	b, err := s.rdb.Get(ctx, "incident:"+id).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var in Incident
	return &in, json.Unmarshal(b, &in)
}

func (s *Store) SetActive(ctx context.Context, in *Incident) error {
	return s.rdb.Set(ctx, "active:"+in.App, in.ID, 24*time.Hour).Err()
}

func (s *Store) ClearActive(ctx context.Context, app string) error {
	return s.rdb.Del(ctx, "active:"+app).Err()
}

func (s *Store) ActiveByApp(ctx context.Context, app string) (*Incident, error) {
	id, err := s.rdb.Get(ctx, "active:"+app).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.Load(ctx, id)
}

func (s *Store) BindSHA(ctx context.Context, sha, id string) error {
	return s.rdb.Set(ctx, "sha:"+sha, id, 24*time.Hour).Err()
}

func (s *Store) BySHA(ctx context.Context, sha string) (*Incident, error) {
	id, err := s.rdb.Get(ctx, "sha:"+sha).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.Load(ctx, id)
}

// DedupeDeployment retorna true se este deployment_uuid não foi visto na janela.
func (s *Store) DedupeDeployment(ctx context.Context, uuid string, ttl time.Duration) (bool, error) {
	return s.rdb.SetNX(ctx, "seen:"+uuid, "1", ttl).Result()
}
