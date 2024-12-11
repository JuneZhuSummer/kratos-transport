package ps_dispatcher

import "github.com/go-redis/redis/v8"

type ProcessFunc func(msg *redis.Message) (interface{}, error)
