package worldarchive

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/Tnze/go-mc/nbt"
	"golang.org/x/sys/unix"
)

func Space(directory string) error {
	var stat unix.Statfs_t
	if err:=unix.Statfs(directory,&stat);err!=nil{return err}
	if uint64(stat.Bavail)*uint64(stat.Bsize)<15*1024*1024*1024{return fmt.Errorf("available disk space is below 15 GiB")}
	return nil
}

func Digest(file string) (string,int64,error) {
	f,err:=os.Open(file);if err!=nil{return "",0,err}
	defer f.Close()
	h:=sha256.New()
	n,err:=io.Copy(h,f)
	return hex.EncodeToString(h.Sum(nil)),n,err
}

func Validate(file,minecraft string) error {
	f,err:=os.Open(file);if err!=nil{return err}
	defer f.Close()
	gz,err:=gzip.NewReader(f);if err!=nil{return err}
	defer gz.Close()
	tr:=tar.NewReader(gz)
	seen:=map[string]bool{}
	level:=false
	var expanded int64
	for {
		h,err:=tr.Next()
		if err==io.EOF{break};if err!=nil{return err}
		name:=strings.TrimSuffix(h.Name,"/")
		if name=="world"&&h.Typeflag==tar.TypeDir{continue}
		if path.Clean(name)!=name||!strings.HasPrefix(name,"world/")||strings.Contains(name,"\\"){return fmt.Errorf("invalid world archive path")}
		if seen[name]{return fmt.Errorf("duplicate archive path")};seen[name]=true
		if h.Typeflag!=tar.TypeReg&&h.Typeflag!=tar.TypeDir{return fmt.Errorf("archive contains a link or special file")}
		expanded+=h.Size
		if h.Size<0||expanded>64*1024*1024*1024{return fmt.Errorf("world archive exceeds expanded size limit")}
		rel:=strings.TrimPrefix(name,"world/")
		root,_,_:=strings.Cut(rel,"/")
		allowed:=map[string]bool{"level.dat":true,"level.dat_old":true,"session.lock":true,"icon.png":true,"region":true,"entities":true,"poi":true,"data":true,"players":true,"playerdata":true,"advancements":true,"stats":true,"datapacks":true,"dimensions":true,"DIM-1":true,"DIM1":true}
		if !allowed[root]{return fmt.Errorf("unexpected world content %q",root)}
		if name=="world/level.dat"{
			if h.Size>16*1024*1024{return fmt.Errorf("level.dat exceeds size limit")}
			compressed,err:=gzip.NewReader(tr);if err!=nil{return err}
			var metadata map[string]any
			_,decodeErr:=nbt.NewDecoder(io.LimitReader(compressed,32*1024*1024)).Decode(&metadata)
			closeErr:=compressed.Close()
			if decodeErr!=nil{return decodeErr};if closeErr!=nil{return closeErr}
			data,ok:=metadata["Data"].(map[string]any);if !ok{return fmt.Errorf("level.dat has no Data compound")}
			version,ok:=data["Version"].(map[string]any);if !ok{return fmt.Errorf("level.dat has no Version compound")}
			name,ok:=version["Name"].(string);if !ok{return fmt.Errorf("level.dat has no version name")}
			if name!=minecraft{return fmt.Errorf("world Minecraft version %q differs from %q",name,minecraft)}
			level=true
		}
	}
	if _,err=io.Copy(io.Discard,gz);err!=nil{return err}
	if !level{return fmt.Errorf("world archive has no level.dat")}
	return nil
}
