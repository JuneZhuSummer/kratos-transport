package batch_operate

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/pprof"
	"sync"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
)

func TestBatchOperate(t *testing.T) {
	var cpuProfile = flag.String("cpuprofile", "batch_operate.prof", "write cpu profile to file")
	flag.Parse()
	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			log.Fatal(err)
		}
		_ = pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	ctx, cancel := context.WithCancel(context.Background())
	wg := sync.WaitGroup{}

	redisCli := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:6379",
		Password:    "",
		DB:          0,
		DialTimeout: time.Second * 5,
	})
	wg.Add(1)
	go func() {
		receiveCount := 0
		subscribe := redisCli.Subscribe(ctx)
		err := subscribe.PSubscribe(ctx, "test_pub_key:*")
		if err != nil {
			t.Logf("subscribe.Subscribe err: %s", err)
		}
		ch := subscribe.Channel()
		for {
			select {
			case <-ctx.Done():
				_ = subscribe.Close()
				t.Logf("receiveCount: %d", receiveCount)
				wg.Done()
				return
			case msg, ok := <-ch:
				if ok {
					receiveCount++
				} else {
					t.Logf("msg: %+v", msg)
				}
			}
		}
	}()

	batchOperate := NewBatchOperate(ctx, redisCli, 500, time.Second)
	for i := 1; i <= 10000; i++ {
		key := fmt.Sprintf("test_key:%d", i)
		batchOperate.Set(key, i, time.Hour)
	}

	for i := 1; i <= 10000; i++ {
		key := fmt.Sprintf("test_hset_key:%d", i)
		batchOperate.HSet(key, i, i)
	}

	for i := 1; i <= 10000; i++ {
		key := fmt.Sprintf("test_hmset_key:%d", i)
		batchOperate.HMSet(key, i, i)
	}

	for i := 1; i <= 10000; i++ {
		key := fmt.Sprintf("test_pub_key:%d", i)
		batchOperate.Publish(key, i)
	}

	for i := 1; i <= 10000; i++ {
		key := "test_lpush_key"
		batchOperate.LPush(key, i)
	}

	for i := 1; i <= 10000; i++ {
		key := "test_rpush_key"
		batchOperate.RPush(key, i)
	}

	for i := 1; i <= 10000; i++ {
		key := fmt.Sprintf("test_key:%d", i)
		batchOperate.Expire(key, time.Minute*time.Duration(i))
	}

	time.Sleep(time.Millisecond * 1000)
	cancel()
	wg.Wait()
}
