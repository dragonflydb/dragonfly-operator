package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/redis/go-redis/v9"
)

const defaultDebounce = 1 * time.Second

type debouncer struct {
	delay time.Duration
	timer *time.Timer
}

func (d *debouncer) Trigger(fn func()) {
	// Debounce: reset the timer so a burst of events triggers a single callback.
	if d.timer != nil {
		if !d.timer.Stop() {
			select {
			case <-d.timer.C:
			default:
			}
		}
	}
	d.timer = time.AfterFunc(d.delay, fn)
}

func main() {
	aclDir := mustGetenv("ACL_DIR")
	aclFile := mustGetenv("ACL_FILE")
	redisHost := mustGetenv("REDIS_HOST")
	redisPort := mustGetenv("REDIS_PORT")
	debounce := parseDuration(os.Getenv("DEBOUNCE"), defaultDebounce)

	addr := redisHost + ":" + redisPort
	log.Printf("acl-watcher starting: aclDir=%s aclFile=%s redisAddr=%s debounce=%s", aclDir, aclFile, addr, debounce)

	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()

	// Initial load
	reloadACL(client, "initial")

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatalf("failed to create watcher: %v", err)
	}
	defer watcher.Close()

	if err := watcher.Add(aclDir); err != nil {
		log.Fatalf("failed to watch directory %s: %v", aclDir, err)
	}

	d := debouncer{delay: debounce}
	trigger := func(reason string) {
		d.Trigger(func() { reloadACL(client, reason) })
	}

	aclFileBase := filepath.Base(aclFile)

	for {
		select {
		case evt, ok := <-watcher.Events:
			if !ok {
				log.Fatalf("watcher events channel closed")
			}
			if !isACLRelated(evt.Name, aclFileBase) {
				continue
			}
			if evt.Has(fsnotify.Create) || evt.Has(fsnotify.Write) || evt.Has(fsnotify.Rename) || evt.Has(fsnotify.Remove) || evt.Has(fsnotify.Chmod) {
				trigger(evt.Op.String())
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				log.Fatalf("watcher errors channel closed")
			}
			log.Printf("watcher error: %v", err)
		}
	}
}

func reloadACL(client *redis.Client, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Do(ctx, "ACL", "LOAD").Err(); err != nil {
		log.Printf("ACL LOAD failed (%s): %v", reason, err)
		return
	}
	log.Printf("ACL LOAD succeeded (%s)", reason)
}

func isACLRelated(path, aclBase string) bool {
	name := filepath.Base(path)
	if name == aclBase {
		return true
	}
	// Secret updates use ..data symlink swaps.
	if strings.HasPrefix(name, "..") {
		return true
	}
	return false
}

func mustGetenv(key string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	log.Fatalf("missing required env var %s", key)
	return ""
}

func parseDuration(value string, def time.Duration) time.Duration {
	if value == "" {
		return def
	}
	dur, err := time.ParseDuration(value)
	if err != nil {
		return def
	}
	return dur
}
