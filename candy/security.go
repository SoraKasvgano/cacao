package candy

import (
	"sync"
	"time"
)

const (
	websocketFailureLimit      = 60
	maxWebsocketFailureEntries = 4096
)

type websocketFailure struct {
	count   int
	expires time.Time
}

// Only unsuccessful authentication attempts consume this allowance. Normal
// reconnects and authenticated tunnel traffic are not rate limited.
type websocketFailureLimiter struct {
	mutex   sync.Mutex
	entries map[string]websocketFailure
}

var websocketFailures = websocketFailureLimiter{entries: make(map[string]websocketFailure)}

func (l *websocketFailureLimiter) allow(ip string) bool {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	entry, ok := l.entries[ip]
	if !ok {
		if len(l.entries) >= maxWebsocketFailureEntries {
			now := time.Now()
			for key, value := range l.entries {
				if now.After(value.expires) {
					delete(l.entries, key)
				}
			}
		}
		return len(l.entries) < maxWebsocketFailureEntries
	}
	if time.Now().After(entry.expires) {
		delete(l.entries, ip)
		return true
	}
	return entry.count < websocketFailureLimit
}

func (l *websocketFailureLimiter) record(ip string) {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	now := time.Now()
	entry, ok := l.entries[ip]
	if !ok || now.After(entry.expires) {
		if len(l.entries) >= maxWebsocketFailureEntries {
			for key, value := range l.entries {
				if now.After(value.expires) {
					delete(l.entries, key)
				}
			}
			// Keep memory bounded under a distributed attack.
			if len(l.entries) >= maxWebsocketFailureEntries && !ok {
				return
			}
		}
		entry = websocketFailure{expires: now.Add(time.Minute)}
	}
	entry.count++
	l.entries[ip] = entry
}
