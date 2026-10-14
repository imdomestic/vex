package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/auth"
	"github.com/imdomestic/chorus-testbed/controller/internal/build"
	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/events"
	"github.com/imdomestic/chorus-testbed/controller/internal/gitmirror"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/rooms"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
)

type Server struct {
	Cfg config.Config
	DB *store.Store
	Git *gitmirror.Mirror
	Builds *build.Manager
	Rooms *rooms.Manager
	Auth *auth.Auth
	Events *events.Bus
}

type handler func(http.ResponseWriter,*http.Request,auth.Identity) error

func New(cfg config.Config,db *store.Store,g *gitmirror.Mirror,b *build.Manager,r *rooms.Manager,a *auth.Auth,e *events.Bus) *Server {
	return &Server{Cfg:cfg,DB:db,Git:g,Builds:b,Rooms:r,Auth:a,Events:e}
}

func (s *Server) Handler() http.Handler {
	mux:=http.NewServeMux()
	register:=func(pattern,permission string,h handler){mux.HandleFunc(pattern,func(w http.ResponseWriter,r *http.Request){
		identity,ok:=s.Auth.Identify(r)
		if permission=="download"&&s.Auth.ValidDownload(r){ok=true;identity=auth.Identity{User:"signed-download"}}
		if permission=="internal"{ok=s.trustedWebhook(r);identity=auth.Identity{}}
		if !ok||permission=="user"&&identity.User==""||permission=="room"&&identity.RoomID==""{writeError(w,model.Failure("unauthorized","请求凭据无效"));return}
		if err:=h(w,r,identity);err!=nil{writeError(w,err)}
	})}
	register("GET /v1/health","user",func(w http.ResponseWriter,r *http.Request,id auth.Identity)error{return respond(w,200,map[string]string{"status":"ok"})})
	register("GET /v1/meta","user",func(w http.ResponseWriter,r *http.Request,id auth.Identity)error{return respond(w,200,model.Meta{Minecraft:s.Cfg.Minecraft.Version,Java:s.Cfg.Minecraft.Java,Zone:s.Cfg.Minecraft.Zone,Loaders:s.Cfg.EnabledLoaders(),PublicBaseURL:s.Cfg.Server.PublicBaseURL,Limits:model.Limits{BuildConcurrency:s.Cfg.Build.MaxConcurrent,BuildCacheSize:s.Cfg.Build.CacheSize,MaxRunning:s.Cfg.Rooms.MaxRunning}})})
	register("POST /v1/git/refresh","user",func(w http.ResponseWriter,r *http.Request,id auth.Identity)error{if err:=s.Rooms.Refresh(r.Context());err!=nil{return err};w.WriteHeader(204);return nil})
	register("GET /v1/branches","user",func(w http.ResponseWriter,r *http.Request,id auth.Identity)error{v,err:=s.Rooms.Branches(r.Context());if err!=nil{return err};return respond(w,200,v)})
	register("GET /v1/commits","user",s.commits)
	register("GET /v1/builds","user",s.buildList)
	register("POST /v1/builds","user",s.buildCreate)
	register("GET /v1/builds/{id}","user",func(w http.ResponseWriter,r *http.Request,id auth.Identity)error{v,err:=s.Builds.Get(r.Context(),r.PathValue("id"));if err!=nil{return err};return respond(w,200,v)})
	register("GET /v1/builds/{id}/log","user",s.buildLog)
	register("GET /v1/builds/{id}/files/{name}","download",s.buildFile)
	register("GET /v1/deps/{sha}","download",s.dependency)
	register("GET /v1/mods/chorus-hop/{mc}/{name}","download",s.hopFile)
	register("GET /v1/rooms","user",s.roomList)
	register("POST /v1/rooms","user",s.roomCreate)
	register("GET /v1/rooms/{id}","either",func(w http.ResponseWriter,r *http.Request,id auth.Identity)error{v,err:=s.Rooms.Get(r.Context(),r.PathValue("id"));if err!=nil{return err};if id.RoomID!=""{if err=s.currentRoomIdentity(r.Context(),id);err!=nil{return err}};return respond(w,200,v)})
	register("POST /v1/rooms/{id}/start","user",s.roomStart)
	register("POST /v1/rooms/{id}/stop","user",s.roomStop)
	register("POST /v1/rooms/{id}/restart","user",s.roomRestart)
	register("PATCH /v1/rooms/{id}","user",s.roomLabel)
	register("DELETE /v1/rooms/{id}/leases/{lease}","either",func(w http.ResponseWriter,r *http.Request,id auth.Identity)error{if err:=s.Rooms.DeleteLease(r.Context(),r.PathValue("id"),r.PathValue("lease"),id);err!=nil{return err};w.WriteHeader(204);return nil})
	register("GET /v1/rooms/{id}/log","user",func(w http.ResponseWriter,r *http.Request,id auth.Identity)error{v,err:=s.Rooms.Log(r.Context(),r.PathValue("id"));if err!=nil{return err};return plain(w,redact(v))})
	register("GET /v1/rooms/{id}/events","user",s.stream)
	register("GET /v1/events","user",s.stream)
	register("GET /v1/nodes","user",func(w http.ResponseWriter,r *http.Request,id auth.Identity)error{v,err:=s.Rooms.Nodes(r.Context());if err!=nil{return err};return respond(w,200,v)})
	register("POST /v1/hooks/mc-router","internal",func(w http.ResponseWriter,r *http.Request,id auth.Identity)error{
		var event struct{Event string `json:"event"`;Timestamp string `json:"timestamp"`;Status string `json:"status"`;Server json.RawMessage `json:"server"`;Player json.RawMessage `json:"player"`;Backend json.RawMessage `json:"backend"`;Client json.RawMessage `json:"client"`;Error json.RawMessage `json:"error"`}
		if err:=decode(r,&event,false);err!=nil{return err}
		go func(){ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second);defer cancel();if err:=s.Rooms.Reconcile(ctx);err!=nil{slog.Error("webhook probe failed","error",err)}}()
		w.WriteHeader(204);return nil
	})
	register("GET /v1/snapshots","user",s.snapshotList)
	register("GET /v1/snapshots/{id}","user",s.snapshotGet)
	register("POST /v1/snapshots/{id}/restore","user",s.snapshotRestore)
	register("POST /v1/snapshots","room",s.snapshotUpload)
	register("GET /v1/snapshots/{id}/download","download",s.snapshotDownload)
	register("GET /v1/assets","user",func(w http.ResponseWriter,r *http.Request,id auth.Identity)error{v,err:=s.Rooms.Assets.List(r.Context());if err!=nil{return err};return respond(w,200,v)})
	register("POST /v1/assets/{id}/revisions","user",s.assetPublish)
	register("POST /v1/assets/{id}/promote","user",s.assetPromote)
	register("PATCH /v1/assets/{id}","user",s.assetState)
	register("POST /v1/assets/{id}/restore","user",s.assetRestore)
	register("GET /v1/assets/{id}/revisions/{rev}/download","download",func(w http.ResponseWriter,r *http.Request,id auth.Identity)error{f,err:=s.Rooms.Assets.OpenWorld(r.Context(),r.PathValue("id"),r.PathValue("rev"));if err!=nil{return err};return serveFile(w,r,f,"application/gzip")})
	register("POST /v1/packs/{id}/revisions","user",s.packPublish)
	register("GET /v1/packs/{id}/revisions/{rev}/download","download",func(w http.ResponseWriter,r *http.Request,id auth.Identity)error{f,err:=s.Rooms.Assets.OpenPack(r.Context(),r.PathValue("id"),r.PathValue("rev"));if err!=nil{return err};return serveFile(w,r,f,"application/zip")})
	register("GET /v1/hop/destinations","room",func(w http.ResponseWriter,r *http.Request,id auth.Identity)error{v,err:=s.Rooms.Destinations(r.Context(),r.URL.Query().Get("fromRoom"),id);if err!=nil{return err};return respond(w,200,v)})
	register("POST /v1/hop","room",func(w http.ResponseWriter,r *http.Request,id auth.Identity)error{
		var body struct{FromRoom string `json:"fromRoom"`;Dest string `json:"dest"`;Player model.Player `json:"player"`}
		if err:=decode(r,&body,false);err!=nil{return err}
		v,status,err:=s.Rooms.Hop(r.Context(),body.FromRoom,body.Dest,body.Player,id);if err!=nil{return err};return respond(w,status,v)
	})
	return mux
}

func (s *Server) trustedWebhook(r *http.Request) bool {
	host,_,err:=net.SplitHostPort(r.RemoteAddr);if err!=nil{return false}
	ip:=net.ParseIP(host)
	for _,source:=range s.Cfg.Rooms.WebhookSources {
		_,network,err:=net.ParseCIDR(source)
		if err==nil&&network.Contains(ip)||source==host{
			trusted,lookupErr:=s.Rooms.Kube.RouterSource(r.Context(),host)
			return lookupErr==nil&&trusted
		}
	}
	return false
}

func (s *Server) currentRoomIdentity(ctx context.Context,id auth.Identity) error {
	room,err:=s.Rooms.Get(ctx,id.RoomID)
	if err!=nil||room.Generation!=id.Generation||room.BuildID!=id.BuildID{return model.Failure("unauthorized","房间凭据周期无效")}
	return nil
}

func decode(r *http.Request,out any,optional bool) error {
	decoder:=json.NewDecoder(io.LimitReader(r.Body,1024*1024))
	decoder.DisallowUnknownFields()
	err:=decoder.Decode(out)
	if err==io.EOF&&optional{return nil}
	if err!=nil{return model.Failure("invalid_request","JSON 请求格式无效")}
	var extra any
	if decoder.Decode(&extra)!=io.EOF{return model.Failure("invalid_request","JSON 请求包含额外内容")}
	return nil
}

func respond(w http.ResponseWriter,status int,value any) error {
	data,err:=json.Marshal(value);if err!=nil{return err}
	w.Header().Set("Content-Type","application/json; charset=utf-8")
	w.Header().Set("Cache-Control","no-store")
	w.WriteHeader(status)
	_,err=w.Write(append(data,'\n'))
	return err
}

func writeError(w http.ResponseWriter,err error) {
	status:=500
	var business *model.Error
	if errors.As(err,&business){
		switch business.Code{case "invalid_request":status=400;case "unauthorized":status=401;case "not_found":status=404;default:status=409}
	}else if errors.Is(err,store.ErrNotFound)||errors.Is(err,os.ErrNotExist){status=404;business=model.Failure("not_found","请求对象不存在")}
	if business==nil{slog.Error("API operation failed","error",err);business=model.Failure("internal_error","服务端操作失败")}
	_ = respond(w,status,struct{Error *model.Error `json:"error"`}{business})
}

func plain(w http.ResponseWriter,value string) error {w.Header().Set("Content-Type","text/plain; charset=utf-8");_,err:=io.WriteString(w,value);return err}

var signedQuery=regexp.MustCompile(`([?&](?:sig|token)=)[^&\s"']+`)
var bearer=regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9._~-]+`)
func redact(text string) string {return bearer.ReplaceAllString(signedQuery.ReplaceAllString(text,"${1}[redacted]"),"${1}[redacted]")}

func (s *Server) commits(w http.ResponseWriter,r *http.Request,id auth.Identity) error {
	q:=r.URL.Query();branch:=q.Get("branch");if branch==""{branch=s.Cfg.Git.DefaultBranch}
	limit:=50
	if q.Has("limit"){n,err:=strconv.Atoi(q.Get("limit"));if err!=nil||n<=0{return model.Failure("invalid_request","limit 必须为正整数")};limit=n}
	commits,err:=s.Git.Commits(r.Context(),branch,q.Get("before"),limit);if err!=nil{return err}
	for i:=range commits{
		commits[i].Builds=map[string]*model.BuildSummary{}
		for _,loader:=range s.Cfg.EnabledLoaders(){
			commits[i].Builds[loader]=nil
			var b model.Build
			err=s.DB.Get(r.Context(),"build",rooms.BuildID(commits[i].SHA,loader),&b)
			if err==nil{commits[i].Builds[loader]=&model.BuildSummary{ID:b.ID,Status:b.Status}}else if !errors.Is(err,store.ErrNotFound){return err}
		}
	}
	return respond(w,200,commits)
}

func (s *Server) buildList(w http.ResponseWriter,r *http.Request,id auth.Identity) error {
	q:=r.URL.Query()
	if sha:=q.Get("sha");sha!=""&&!rooms.ValidSHA(sha){return model.Failure("invalid_request","sha 无效")}
	if loader:=q.Get("loader");loader!=""&&!s.Cfg.Loaders[loader].Enabled{return model.Failure("invalid_request","loader 无效")}
	if status:=q.Get("status");status!=""&&status!="queued"&&status!="building"&&status!="ready"&&status!="failed"{return model.Failure("invalid_request","构建状态无效")}
	builds,err:=s.Builds.List(r.Context());if err!=nil{return err}
	result:=[]model.Build{}
	for _,b:=range builds{if (q.Get("sha")==""||q.Get("sha")==b.SHA)&&(q.Get("loader")==""||q.Get("loader")==b.Loader)&&(q.Get("status")==""||q.Get("status")==b.Status){result=append(result,b)}}
	sort.Slice(result,func(i,j int)bool{if result[i].QueuedAt.Equal(result[j].QueuedAt){return result[i].ID<result[j].ID};return result[i].QueuedAt.After(result[j].QueuedAt)})
	return respond(w,200,result)
}

func (s *Server) buildCreate(w http.ResponseWriter,r *http.Request,id auth.Identity) error {
	var req struct{SHA string `json:"sha"`;Loader string `json:"loader"`;Force bool `json:"force"`}
	if err:=decode(r,&req,false);err!=nil{return err}
	b,created,err:=s.Builds.Request(r.Context(),req.SHA,req.Loader,req.Force);if err!=nil{return err}
	status:=200;if created{status=202}
	return respond(w,status,b)
}

func (s *Server) buildLog(w http.ResponseWriter,r *http.Request,id auth.Identity) error {
	f,err:=s.Builds.OpenLog(r.Context(),r.PathValue("id"));if err!=nil{return err};defer f.Close()
	data,err:=io.ReadAll(io.LimitReader(f,64*1024*1024));if err!=nil{return err}
	return plain(w,redact(string(data)))
}

func serveFile(w http.ResponseWriter,r *http.Request,f *os.File,contentType string) error {
	defer f.Close()
	stat,err:=f.Stat();if err!=nil{return err}
	w.Header().Set("Content-Type",contentType)
	w.Header().Set("X-Content-Type-Options","nosniff")
	http.ServeContent(w,r,stat.Name(),stat.ModTime(),f)
	return nil
}

func (s *Server) buildFile(w http.ResponseWriter,r *http.Request,id auth.Identity) error {f,err:=s.Builds.OpenFile(r.Context(),r.PathValue("id"),r.PathValue("name"));if err!=nil{return err};return serveFile(w,r,f,"application/java-archive")}
func (s *Server) dependency(w http.ResponseWriter,r *http.Request,id auth.Identity) error {f,err:=s.Builds.OpenDependency(r.Context(),r.PathValue("sha"));if err!=nil{return err};return serveFile(w,r,f,"application/java-archive")}

func (s *Server) hopFile(w http.ResponseWriter,r *http.Request,id auth.Identity) error {
	mc,name:=r.PathValue("mc"),r.PathValue("name")
	if !safeName(mc)||!safeName(name)||!strings.HasSuffix(name,".jar"){return model.Failure("invalid_request","文件路径无效")}
	f,err:=os.Open(filepath.Join(s.Cfg.Server.DataDir,"mods","chorus-hop",mc,name));if err!=nil{return err}
	return serveFile(w,r,f,"application/java-archive")
}

func safeName(value string) bool {return value!=""&&value!="."&&value!=".."&&!strings.ContainsAny(value,"/\\\x00")}

func (s *Server) roomList(w http.ResponseWriter,r *http.Request,id auth.Identity) error {
	q:=r.URL.Query()
	for key,allowed:=range map[string]string{"kind":"branch commit asset","state":"stopped queued building starting ready stopping archiving closed failed lost"}{
		if value:=q.Get(key);value!=""&&!contains(strings.Fields(allowed),value){return model.Failure("invalid_request","房间过滤条件无效")}
		if len(q[key])>1{return model.Failure("invalid_request","房间过滤条件必须为单值")}
	}
	all,err:=s.Rooms.List(r.Context());if err!=nil{return err}
	result:=[]model.Room{}
	for _,room:=range all{if (q.Get("kind")==""||q.Get("kind")==room.Kind)&&(q.Get("state")==""||q.Get("state")==room.State){result=append(result,room)}}
	return respond(w,200,result)
}

func contains(values []string,value string) bool {for _,v:=range values{if v==value{return true}};return false}

func (s *Server) roomCreate(w http.ResponseWriter,r *http.Request,id auth.Identity) error {
	var req struct{Kind string `json:"kind"`;SHA string `json:"sha"`;Loader string `json:"loader"`;Seed string `json:"seed"`;Asset string `json:"asset"`}
	if err:=decode(r,&req,false);err!=nil{return err}
	room,created,err:=s.Rooms.Create(r.Context(),req.Kind,req.SHA,req.Loader,req.Seed,req.Asset);if err!=nil{return err}
	status:=200;if created{status=201}
	return respond(w,status,room)
}

func (s *Server) roomStart(w http.ResponseWriter,r *http.Request,id auth.Identity) error {
	var req struct{Seed *string `json:"seed"`}
	if err:=decode(r,&req,true);err!=nil{return err}
	result,status,err:=s.Rooms.Start(r.Context(),r.PathValue("id"),req.Seed,id);if err!=nil{return err}
	return respond(w,status,result)
}

func (s *Server) roomStop(w http.ResponseWriter,r *http.Request,id auth.Identity) error {return s.stop(w,r,false)}
func (s *Server) roomRestart(w http.ResponseWriter,r *http.Request,id auth.Identity) error {return s.stop(w,r,true)}
func (s *Server) stop(w http.ResponseWriter,r *http.Request,restart bool) error {room,status,err:=s.Rooms.Stop(r.Context(),r.PathValue("id"),restart);if err!=nil{return err};return respond(w,status,room)}
func (s *Server) roomLabel(w http.ResponseWriter,r *http.Request,id auth.Identity) error {
	var req struct{Label string `json:"label"`}
	if err:=decode(r,&req,false);err!=nil{return err}
	room,status,err:=s.Rooms.Label(r.Context(),r.PathValue("id"),req.Label);if err!=nil{return err}
	return respond(w,status,room)
}

func (s *Server) stream(w http.ResponseWriter,r *http.Request,id auth.Identity) error {
	first,ch,cancel,err:=s.Events.Subscribe(r.Context(),r.PathValue("id"));if err!=nil{return err}
	defer cancel()
	flusher,ok:=w.(http.Flusher);if !ok{return fmt.Errorf("HTTP streaming is unavailable")}
	w.Header().Set("Content-Type","text/event-stream")
	w.Header().Set("Cache-Control","no-cache")
	w.Header().Set("X-Accel-Buffering","no")
	write:=func(e events.Event)error{_,err:=fmt.Fprintf(w,"id: %d\nevent: %s\ndata: %s\n\n",e.ID,e.Name,e.Data);flusher.Flush();return err}
	if err=write(first);err!=nil{return nil}
	ticker:=time.NewTicker(15*time.Second);defer ticker.Stop()
	for{select{
	case <-r.Context().Done():return nil
	case event,open:=<-ch:if !open{return nil};if write(event)!=nil{return nil}
	case <-ticker.C:if _,err=io.WriteString(w,": ping\n\n");err!=nil{return nil};flusher.Flush()
	}}
}
