package events

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
)

type Event struct {
	ID      uint64
	Name    string
	Data    []byte
	RoomID  string
	BuildID string
}
type subscription struct {
	room    string
	channel chan Event
}
type Bus struct {
	mu   sync.Mutex
	db   *store.Store
	next uint64
	subs map[*subscription]struct{}
	last map[string]string
}

func New(db *store.Store) *Bus {
	return &Bus{db: db, subs: map[*subscription]struct{}{}, last: map[string]string{}}
}

func (b *Bus) event(name string, data any, room, build string) (Event, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return Event{}, err
	}
	b.next++
	return Event{ID: b.next, Name: name, Data: encoded, RoomID: room, BuildID: build}, nil
}

func (b *Bus) Subscribe(ctx context.Context, roomID string) (Event, <-chan Event, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rooms, err := store.All[model.Room](ctx, b.db, "room")
	if err != nil {
		return Event{}, nil, nil, err
	}
	builds, err := store.All[model.Build](ctx, b.db, "build")
	if err != nil {
		return Event{}, nil, nil, err
	}
	if roomID != "" {
		filtered := []model.Room{}
		buildID := ""
		for _, room := range rooms {
			if room.ID == roomID {
				filtered = append(filtered, room)
				buildID = room.BuildID
			}
		}
		if len(filtered) == 0 {
			return Event{}, nil, nil, store.ErrNotFound
		}
		rooms = filtered
		filteredBuilds := []model.Build{}
		for _, build := range builds {
			if build.ID == buildID {
				filteredBuilds = append(filteredBuilds, build)
			}
		}
		builds = filteredBuilds
	}
	first, err := b.event("sync", model.Sync{Rooms: rooms, Builds: builds}, roomID, "")
	if err != nil {
		return Event{}, nil, nil, err
	}
	sub := &subscription{room: roomID, channel: make(chan Event, 64)}
	b.subs[sub] = struct{}{}
	closeSub := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[sub]; ok {
			delete(b.subs, sub)
			close(sub.channel)
		}
	}
	return first, sub.channel, closeSub, nil
}

func (b *Bus) publishLocked(ctx context.Context, event Event) error {
	for sub := range b.subs {
		if sub.room != "" && event.RoomID != sub.room {
			if event.Name != "build" {
				continue
			}
			var room model.Room
			if err := b.db.Get(ctx, "room", sub.room, &room); err != nil {
				return err
			}
			if room.BuildID != event.BuildID {
				continue
			}
		}
		select {
		case sub.channel <- event:
		default:
			delete(b.subs, sub)
			close(sub.channel)
		}
	}
	return nil
}

func (b *Bus) Publish(ctx context.Context, name string, data any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	event, err := b.event(name, data, "", "")
	if err != nil {
		return err
	}
	return b.publishLocked(ctx, event)
}

func (b *Bus) Poll(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, kind := range []string{"room", "build"} {
		items, err := b.db.List(ctx, kind)
		if err != nil {
			return err
		}
		for _, raw := range items {
			var object struct {
				ID      string `json:"id"`
				BuildID string `json:"buildId"`
				State   string `json:"state"`
			}
			if err = json.Unmarshal(raw, &object); err != nil {
				return err
			}
			key := kind + ":" + object.ID
			if b.last[key] == string(raw) {
				continue
			}
			b.last[key] = string(raw)
			b.next++
			event := Event{ID: b.next, Name: kind, Data: raw}
			if kind == "room" {
				event.RoomID = object.ID
			} else {
				event.BuildID = object.ID
			}
			if err = b.publishLocked(ctx, event); err != nil {
				return err
			}
			if kind == "room" && (object.State == "ready" || object.State == "failed") {
				var room model.Room
				if err = json.Unmarshal(raw, &room); err != nil {
					return err
				}
				var data any
				if object.State == "ready" {
					data = struct {
						ID         string `json:"id"`
						Generation int64  `json:"generation"`
						Host       string `json:"host"`
						Port       int    `json:"port"`
						BuildID    string `json:"buildId"`
					}{room.ID, room.Generation, room.Host, room.Port, room.BuildID}
				} else {
					data = struct {
						ID         string       `json:"id"`
						Generation int64        `json:"generation"`
						Error      *model.Error `json:"error"`
					}{room.ID, room.Generation, room.LastError}
				}
				e, err := b.event(object.State, data, room.ID, room.BuildID)
				if err != nil {
					return err
				}
				if err = b.publishLocked(ctx, e); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (b *Bus) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := b.Poll(ctx); err != nil {
				return fmt.Errorf("events: %w", err)
			}
		}
	}
}
