package events

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

type Event struct {
	ID      uint64
	Name    string
	Project string
	Data    []byte
	RoomID  string
	BuildID string
}

type subscription struct {
	projects map[string]bool
	room     string
	build    string
	channel  chan Event
}

type Bus struct {
	mu       sync.Mutex
	db       *store.Store
	projects []string
	next     uint64
	subs     map[*subscription]struct{}
	last     map[string]Event
}

func New(db *store.Store, projects ...string) *Bus {
	return &Bus{db: db, projects: slices.Clone(projects), subs: map[*subscription]struct{}{}, last: map[string]Event{}}
}

func (b *Bus) event(project, name string, data any, room, build string) (Event, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return Event{}, err
	}
	var object map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &object); err != nil {
		return Event{}, err
	}
	if object == nil {
		return Event{}, fmt.Errorf("event payload must be an object")
	}
	object["project"], err = json.Marshal(project)
	if err != nil {
		return Event{}, err
	}
	encoded, err = json.Marshal(object)
	if err != nil {
		return Event{}, err
	}
	b.next++
	return Event{ID: b.next, Name: name, Project: project, Data: encoded, RoomID: room, BuildID: build}, nil
}

func (b *Bus) Subscribe(ctx context.Context, projects []string, roomID string) (Event, <-chan Event, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if roomID != "" && len(projects) != 1 {
		return Event{}, nil, nil, fmt.Errorf("room subscription requires one project")
	}
	allowed := map[string]bool{}
	for _, project := range projects {
		if !slices.Contains(b.projects, project) {
			return Event{}, nil, nil, store.ErrNotFound
		}
		allowed[project] = true
	}
	data := struct {
		model.Sync
		Projects  []string             `json:"projects"`
		Snapshots []model.Snapshot     `json:"snapshots"`
		Worlds    []model.World        `json:"worlds"`
		Packs     []model.PackRevision `json:"packs"`
	}{Sync: model.Sync{Rooms: []model.Room{}, Builds: []model.Build{}}, Projects: slices.Clone(projects), Snapshots: []model.Snapshot{}, Worlds: []model.World{}, Packs: []model.PackRevision{}}
	err := b.db.View(ctx, func(tx *store.View) error {
		for _, project := range projects {
			access, err := tx.WithProject(project)
			if err != nil {
				return err
			}
			rooms, err := store.All[model.Room](ctx, access, "room")
			if err != nil {
				return err
			}
			builds, err := store.All[model.Build](ctx, access, "build")
			if err != nil {
				return err
			}
			data.Rooms = append(data.Rooms, rooms...)
			data.Builds = append(data.Builds, builds...)
			if roomID != "" {
				continue
			}
			snapshots, err := store.All[model.Snapshot](ctx, access, "snapshot")
			if err != nil {
				return err
			}
			worlds, err := store.All[model.World](ctx, access, "world")
			if err != nil {
				return err
			}
			packs, err := store.All[model.PackRevision](ctx, access, "pack_revision")
			if err != nil {
				return err
			}
			data.Snapshots = append(data.Snapshots, snapshots...)
			data.Worlds = append(data.Worlds, worlds...)
			data.Packs = append(data.Packs, packs...)
		}
		return nil
	})
	if err != nil {
		return Event{}, nil, nil, err
	}
	buildID := ""
	if roomID != "" {
		rooms := []model.Room{}
		for _, room := range data.Rooms {
			if room.ID == roomID {
				rooms = append(rooms, room)
				buildID = room.BuildID
			}
		}
		if len(rooms) == 0 {
			return Event{}, nil, nil, store.ErrNotFound
		}
		data.Rooms = rooms
		builds := []model.Build{}
		for _, build := range data.Builds {
			if build.ID == buildID {
				builds = append(builds, build)
			}
		}
		data.Builds = builds
	}
	project := ""
	if len(projects) == 1 {
		project = projects[0]
	}
	first, err := b.event(project, "sync", data, roomID, buildID)
	if err != nil {
		return Event{}, nil, nil, err
	}
	sub := &subscription{projects: allowed, room: roomID, build: buildID, channel: make(chan Event, 64)}
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

func (b *Bus) publishLocked(event Event) {
	for sub := range b.subs {
		if !sub.projects[event.Project] {
			continue
		}
		if sub.room != "" {
			if event.RoomID == sub.room {
				if event.Name == "room" {
					sub.build = event.BuildID
				}
				if event.Name == "room_deleted" {
					sub.build = ""
				}
			} else if (event.Name != "build" && event.Name != "build_deleted") || sub.build == "" || event.BuildID != sub.build {
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
}

func (b *Bus) Publish(ctx context.Context, project, name string, data any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !slices.Contains(b.projects, project) {
		return store.ErrNotFound
	}
	event, err := b.event(project, name, data, "", "")
	if err != nil {
		return err
	}
	b.publishLocked(event)
	return nil
}

func (b *Bus) Poll(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	current := map[string]Event{}
	err := b.db.View(ctx, func(tx *store.View) error {
		for _, project := range b.projects {
			access, err := tx.WithProject(project)
			if err != nil {
				return err
			}
			for _, kind := range []string{"room", "build", "snapshot", "world", "pack_revision"} {
				items, err := access.List(ctx, kind)
				if err != nil {
					return err
				}
				for _, raw := range items {
					var object struct {
						ID      string `json:"id"`
						Rev     string `json:"rev"`
						BuildID string `json:"buildId"`
					}
					if err = json.Unmarshal(raw, &object); err != nil {
						return err
					}
					name := kind
					key := project + "/" + kind + ":" + object.ID
					if kind == "pack_revision" {
						name = "pack"
						key += ":" + object.Rev
					}
					event := Event{Project: project, Name: name, Data: raw}
					if kind == "room" {
						event.RoomID = object.ID
						event.BuildID = object.BuildID
					}
					if kind == "build" {
						event.BuildID = object.ID
					}
					current[key] = event
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for sub := range b.subs {
		if sub.room == "" {
			continue
		}
		for project := range sub.projects {
			if room, ok := current[project+"/room:"+sub.room]; ok {
				sub.build = room.BuildID
			}
		}
	}
	keys := make([]string, 0, len(current))
	for key := range current {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		item := current[key]
		if old, ok := b.last[key]; ok && string(old.Data) == string(item.Data) {
			continue
		}
		event, err := b.event(item.Project, item.Name, json.RawMessage(item.Data), item.RoomID, item.BuildID)
		if err != nil {
			return err
		}
		b.publishLocked(event)
		if item.Name == "room" {
			var room model.Room
			if err = json.Unmarshal(item.Data, &room); err != nil {
				return err
			}
			if room.State != "ready" && room.State != "failed" {
				continue
			}
			payload := map[string]any{"id": room.ID, "generation": room.Generation}
			if room.State == "ready" {
				payload["host"] = room.Host
				payload["port"] = room.Port
				payload["buildId"] = room.BuildID
			} else {
				payload["error"] = room.LastError
			}
			level, err := b.event(item.Project, room.State, payload, room.ID, room.BuildID)
			if err != nil {
				return err
			}
			b.publishLocked(level)
		}
	}
	for key, old := range b.last {
		if _, ok := current[key]; ok || (old.Name != "room" && old.Name != "build") {
			continue
		}
		id := old.RoomID
		if old.Name == "build" {
			id = old.BuildID
		}
		event, err := b.event(old.Project, old.Name+"_deleted", map[string]string{"id": id}, old.RoomID, old.BuildID)
		if err != nil {
			return err
		}
		b.publishLocked(event)
	}
	b.last = current
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
