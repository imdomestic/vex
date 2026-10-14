package rooms

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/imdomestic/chorus-testbed/controller/internal/archive"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
)

func (m *Manager) PublishSnapshot(ctx context.Context,snapshot model.Snapshot,source string) (model.Snapshot,bool,error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var old model.Snapshot
	err:=m.DB.Get(ctx,"snapshot",snapshot.ID,&old)
	if err==nil{
		if old.RoomID!=snapshot.RoomID||old.RoomGeneration!=snapshot.RoomGeneration||old.BuildID!=snapshot.BuildID||old.SHA256!=snapshot.SHA256||old.Size!=snapshot.Size{return old,false,model.Failure("conflict","该周期已经发布不同的快照内容")}
		return old,false,nil
	}
	if !errors.Is(err,store.ErrNotFound){return snapshot,false,err}
	room,err:=m.Get(ctx,snapshot.RoomID);if err!=nil{return snapshot,false,err}
	rt,err:=m.runtime(ctx,room.ID);if err!=nil{return snapshot,false,err}
	if room.Generation!=snapshot.RoomGeneration||room.BuildID!=snapshot.BuildID||room.State!="archiving"||rt.StoppedAt==nil{return snapshot,false,model.Failure("conflict","当前房间没有匹配的归档任务")}
	snapshot.CapturedAt=*rt.StoppedAt
	if room.Asset!=nil&&room.SeedResolved!=nil{snapshot.Asset=&model.PackRef{ID:*room.Asset,Rev:room.SeedResolved.Rev}}
	dest:=SnapshotPath(m.Cfg.Server.DataDir,snapshot)
	if err=os.MkdirAll(filepath.Dir(dest),0700);err!=nil{return snapshot,false,err}
	if err=os.Rename(source,dest);err!=nil{return snapshot,false,err}
	room.LastSnapshot=&snapshot.CapturedAt
	err=m.DB.Update(ctx,func(tx *store.Tx)error{
		if err:=tx.Put(ctx,"snapshot",snapshot.ID,snapshot);err!=nil{return err}
		if err:=archive.QueueSnapshot(ctx,tx,m.Cfg.Server.DataDir,snapshot);err!=nil{return err}
		return tx.Put(ctx,"room",room.ID,room)
	})
	return snapshot,true,err
}
