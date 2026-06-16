package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// CartStore guarda o carrinho no Redis como um hash productID->quantity, com TTL.
type CartStore struct{ rdb *redis.Client }

const cartTTL = 7 * 24 * time.Hour

func cartKey(userID int64) string { return fmt.Sprintf("cart:%d", userID) }

func (c *CartStore) Add(ctx context.Context, userID, productID int64) error {
	k := cartKey(userID)
	if err := c.rdb.HIncrBy(ctx, k, strconv.FormatInt(productID, 10), 1).Err(); err != nil {
		return err
	}
	return c.rdb.Expire(ctx, k, cartTTL).Err()
}

func (c *CartStore) Remove(ctx context.Context, userID, productID int64) error {
	return c.rdb.HDel(ctx, cartKey(userID), strconv.FormatInt(productID, 10)).Err()
}

func (c *CartStore) List(ctx context.Context, userID int64) ([]CartItem, error) {
	m, err := c.rdb.HGetAll(ctx, cartKey(userID)).Result()
	if err != nil {
		return nil, err
	}
	items := []CartItem{}
	for pid, qty := range m {
		id, _ := strconv.ParseInt(pid, 10, 64)
		q, _ := strconv.Atoi(qty)
		if id > 0 && q > 0 {
			items = append(items, CartItem{ProductID: id, Quantity: q})
		}
	}
	return items, nil
}

func (c *CartStore) Clear(ctx context.Context, userID int64) error {
	return c.rdb.Del(ctx, cartKey(userID)).Err()
}
