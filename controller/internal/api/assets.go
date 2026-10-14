package api

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/imdomestic/chorus-testbed/controller/internal/auth"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/worldarchive"
)

func (s *Server) assetPublish(w http.ResponseWriter,r *http.Request,id auth.Identity) error {
	if err:=worldarchive.Space(s.Cfg.Server.DataDir);err!=nil{return model.Failure("archive_failed",err.Error())}
	r.Body=http.MaxBytesReader(w,r.Body,32*1024*1024*1024+1024*1024)
	reader,err:=r.MultipartReader();if err!=nil{return model.Failure("invalid_request","资产发布需要 multipart/form-data")}
	file,err:=os.CreateTemp(filepath.Join(s.Cfg.Server.DataDir,".work"),"asset-upload-*.tar.gz");if err!=nil{return err}
	defer os.Remove(file.Name());defer file.Close()
	var metadata model.AssetMetadata
	seen:=map[string]bool{}
	for {
		part,err:=reader.NextPart();if err==io.EOF{break};if err!=nil{return model.Failure("invalid_request",err.Error())}
		name:=part.FormName()
		if seen[name]{part.Close();return model.Failure("invalid_request","multipart 字段重复")};seen[name]=true
		mediaType,_,err:=mime.ParseMediaType(part.Header.Get("Content-Type"));if err!=nil{part.Close();return model.Failure("invalid_request","multipart 内容类型无效")}
		switch name {
		case "asset":
			if mediaType!="application/json"{part.Close();return model.Failure("invalid_request","asset 字段需要 application/json")}
			decoder:=json.NewDecoder(io.LimitReader(part,1024*1024));decoder.DisallowUnknownFields()
			if err=decoder.Decode(&metadata);err!=nil{part.Close();return model.Failure("invalid_request","资产元数据 JSON 无效")}
			var extra any
			if decoder.Decode(&extra)!=io.EOF{part.Close();return model.Failure("invalid_request","资产元数据包含额外内容")}
		case "world":
			if mediaType!="application/gzip"{part.Close();return model.Failure("invalid_request","world 字段需要 application/gzip")}
			if _,err=io.Copy(file,part);err!=nil{part.Close();return err}
		default:part.Close();return model.Failure("invalid_request","未知的 multipart 字段")
		}
		if err=part.Close();err!=nil{return err}
	}
	if !seen["world"]||!seen["asset"]||metadata.ID!=r.PathValue("id"){return model.Failure("invalid_request","资产字段缺失或 ID 不一致")}
	if err=file.Sync();err!=nil{return err};if err=file.Close();err!=nil{return err}
	value,created,err:=s.Rooms.Assets.Publish(r.Context(),metadata,file.Name());if err!=nil{return err}
	status:=200;if created{status=201};return respond(w,status,value)
}

func (s *Server) assetPromote(w http.ResponseWriter,r *http.Request,id auth.Identity) error {
	var body struct{Edit string `json:"edit"`}
	if err:=decode(r,&body,false);err!=nil{return err}
	value,created,err:=s.Rooms.Assets.Promote(r.Context(),r.PathValue("id"),body.Edit);if err!=nil{return err}
	status:=200;if created{status=201};return respond(w,status,value)
}

func (s *Server) assetState(w http.ResponseWriter,r *http.Request,id auth.Identity) error {
	var body struct{State string `json:"state"`}
	if err:=decode(r,&body,false);err!=nil{return err}
	value,accepted,err:=s.Rooms.Assets.SetState(r.Context(),r.PathValue("id"),body.State);if err!=nil{return err}
	status:=200;if accepted{status=202};return respond(w,status,value)
}

func (s *Server) assetRestore(w http.ResponseWriter,r *http.Request,id auth.Identity) error {
	var body struct{}
	if err:=decode(r,&body,true);err!=nil{return err}
	value,accepted,err:=s.Rooms.Assets.RequestRestore(r.Context(),r.PathValue("id"));if err!=nil{return err}
	status:=200;if accepted{status=202};return respond(w,status,value)
}

func (s *Server) packPublish(w http.ResponseWriter,r *http.Request,id auth.Identity) error {
	if r.Header.Get("Content-Type")!="application/zip"{return model.Failure("invalid_request","资源包需要 application/zip")}
	if err:=worldarchive.Space(s.Cfg.Server.DataDir);err!=nil{return model.Failure("archive_failed",err.Error())}
	file,err:=os.CreateTemp(filepath.Join(s.Cfg.Server.DataDir,".work"),"pack-upload-*.zip");if err!=nil{return err}
	defer os.Remove(file.Name());defer file.Close()
	if _,err=io.Copy(file,http.MaxBytesReader(w,r.Body,8*1024*1024*1024));err!=nil{return err}
	if err=file.Sync();err!=nil{return err};if err=file.Close();err!=nil{return err}
	value,created,err:=s.Rooms.Assets.PublishPack(r.Context(),r.PathValue("id"),file.Name());if err!=nil{return err}
	status:=200;if created{status=201};return respond(w,status,value)
}
