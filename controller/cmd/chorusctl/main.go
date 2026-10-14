package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func main(){if err:=run();err!=nil{fmt.Fprintln(os.Stderr,err);os.Exit(1)}}

func run() error {
	flags:=flag.NewFlagSet("chorusctl",flag.ContinueOnError)
	base:=flags.String("url","http://encore.inner.imdomestic.com:8790","controller URL")
	tokenFile:=flags.String("token-file",os.Getenv("CHORUS_TOKEN_FILE"),"token file")
	usersFile:=flags.String("users-file","","server users TOML file")
	user:=flags.String("user","linwhite","user name in users file")
	timeout:=flags.Duration("timeout",20*time.Minute,"request deadline, including uploads and validation")
	if err:=flags.Parse(os.Args[1:]);err!=nil{return err}
	args:=flags.Args()
	if len(args)==0{return fmt.Errorf("usage: chorusctl [options] builds|rooms|asset|pack|git|request|health|meta|nodes")}
	if *timeout<=0{return fmt.Errorf("timeout must be positive")}
	token:=""
	if *usersFile!=""{
		data,err:=os.ReadFile(*usersFile);if err!=nil{return err}
		var conf struct{Users map[string]string `toml:"users"`}
		if err=toml.Unmarshal(data,&conf);err!=nil{return fmt.Errorf("invalid users configuration")}
		token=conf.Users[*user]
	}else if *tokenFile!=""{data,err:=os.ReadFile(*tokenFile);if err!=nil{return err};token=strings.TrimSpace(string(data))}
	if token==""{return fmt.Errorf("a token file or users file is required")}
	method,path:="GET",""
	var body any
	var upload *streamUpload
	switch args[0]{
	case "health","meta","nodes":path="/v1/"+args[0]
	case "git":
		if len(args)!=2||args[1]!="refresh"{return fmt.Errorf("usage: git refresh")};method="POST";path="/v1/git/refresh"
	case "builds":
		path="/v1/builds"
		if len(args)>1&&args[1]!="list"{
			if args[1]=="create"{
				fs:=flag.NewFlagSet("builds create",flag.ContinueOnError);sha:=fs.String("commit","","commit SHA");loader:=fs.String("loader","fabric","loader");force:=fs.Bool("force",false,"new attempt");if err:=fs.Parse(args[2:]);err!=nil{return err}
				method="POST";body=map[string]any{"sha":*sha,"loader":*loader,"force":*force}
			}else{path+="/"+url.PathEscape(args[1]);if len(args)==3&&args[2]=="log"{path+="/log"}}
		}
	case "rooms":
		path="/v1/rooms"
		if len(args)>1&&args[1]!="list"{
			switch args[1]{
			case "create":
				fs:=flag.NewFlagSet("rooms create",flag.ContinueOnError);sha:=fs.String("commit","","commit SHA");loader:=fs.String("loader","fabric","loader");seed:=fs.String("seed","empty","world seed");if err:=fs.Parse(args[2:]);err!=nil{return err}
				method="POST";body=map[string]any{"kind":"commit","sha":*sha,"loader":*loader,"seed":*seed}
			case "start","stop","restart","log","show":
				if len(args)!=3{return fmt.Errorf("rooms %s requires a room ID",args[1])};path+="/"+url.PathEscape(args[2]);if args[1]!="show"{path+="/"+args[1]};if args[1]!="show"&&args[1]!="log"{method="POST"}
			default:return fmt.Errorf("unknown rooms operation")
			}
		}
	case "asset","pack":
		var err error
		method,path,body,upload,err=publicationCommand(args)
		if err!=nil{return err}
		if upload!=nil{defer upload.File.Close()}
	case "request":
		if len(args)<3||len(args)>4{return fmt.Errorf("usage: request METHOD /v1/path [JSON-file]")}
		method,path=args[1],args[2]
		if len(args)==4{data,err:=os.ReadFile(args[3]);if err!=nil{return err};if !json.Valid(data){return fmt.Errorf("invalid JSON input")};body=json.RawMessage(data)}
	default:return fmt.Errorf("unknown command %q",args[0])
	}
	var reader io.Reader
	if body!=nil{data,err:=json.Marshal(body);if err!=nil{return err};reader=bytes.NewReader(data)}
	if upload!=nil{reader=upload.Reader}
	req,err:=http.NewRequest(method,strings.TrimRight(*base,"/")+path,reader);if err!=nil{return err}
	req.Header.Set("Authorization","Bearer "+token)
	if body!=nil{req.Header.Set("Content-Type","application/json")}
	if upload!=nil{req.Header.Set("Content-Type",upload.ContentType);req.ContentLength=upload.ContentLength}
	client:=http.Client{Timeout:*timeout,CheckRedirect:func(req *http.Request,via []*http.Request)error{return http.ErrUseLastResponse},Transport:&http.Transport{Proxy:http.ProxyFromEnvironment,DialContext:(&net.Dialer{Timeout:10*time.Second}).DialContext,TLSHandshakeTimeout:10*time.Second}}
	response,err:=client.Do(req);if err!=nil{return err}
	defer response.Body.Close()
	data,err:=io.ReadAll(response.Body);if err!=nil{return err}
	if response.StatusCode<200||response.StatusCode>=300{return responseError(response.StatusCode,data)}
	if upload!=nil{if err=upload.Verify(data);err!=nil{return err}}
	if len(data)>0{_,err=os.Stdout.Write(data)}
	return err
}
