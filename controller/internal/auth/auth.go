package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Auth struct { users map[string]string; key []byte }

type Identity struct {
	User string `json:"user,omitempty"`
	RoomID string `json:"roomId,omitempty"`
	Generation int64 `json:"generation,omitempty"`
	BuildID string `json:"buildId,omitempty"`
	Expires int64 `json:"expires,omitempty"`
}

func Load(usersFile, keyFile string) (*Auth,error) {
	data,err := os.ReadFile(usersFile)
	if err != nil { return nil,err }
	var conf struct { Users map[string]string `toml:"users"` }
	if err = toml.Unmarshal(data,&conf); err != nil { return nil,fmt.Errorf("invalid users configuration") }
	if len(conf.Users) == 0 { return nil,fmt.Errorf("users configuration is empty") }
	for name,token := range conf.Users { if name == "" || len(token) < 32 { return nil,fmt.Errorf("user identity or token length is invalid") } }
	key,err := os.ReadFile(keyFile)
	if err != nil { return nil,err }
	if len(key) < 32 { return nil,fmt.Errorf("signing key requires at least 32 bytes") }
	return &Auth{users:conf.Users,key:key},nil
}

func (a *Auth) sum(text string) []byte {
	m := hmac.New(sha256.New,a.key)
	m.Write([]byte(text))
	return m.Sum(nil)
}

func (a *Auth) Identify(r *http.Request) (Identity,bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header,"Bearer ") { return Identity{},false }
	token := strings.TrimPrefix(header,"Bearer ")
	for name,value := range a.users {
		if subtle.ConstantTimeCompare([]byte(token),[]byte(value)) == 1 { return Identity{User:name},true }
	}
	payload,signature,ok := strings.Cut(token,".")
	if !ok { return Identity{},false }
	sig,err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(sig,a.sum("room\n"+payload)) { return Identity{},false }
	data,err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil { return Identity{},false }
	var identity Identity
	if json.Unmarshal(data,&identity) != nil || identity.User != "" || identity.RoomID == "" || identity.Generation <= 0 || identity.BuildID == "" || time.Now().Unix() >= identity.Expires { return Identity{},false }
	return identity,true
}

func (a *Auth) RoomToken(roomID string,generation int64,buildID string,expires time.Time) (string,error) {
	data,err := json.Marshal(Identity{RoomID:roomID,Generation:generation,BuildID:buildID,Expires:expires.Unix()})
	if err != nil { return "",err }
	payload := base64.RawURLEncoding.EncodeToString(data)
	return payload+"."+base64.RawURLEncoding.EncodeToString(a.sum("room\n"+payload)),nil
}

func (a *Auth) SignURL(base,path string,expires time.Time) (string,error) {
	u,err := url.Parse(strings.TrimRight(base,"/")+path)
	if err != nil { return "",err }
	exp := strconv.FormatInt(expires.Unix(),10)
	q := u.Query()
	q.Set("exp",exp)
	q.Set("sig",hex.EncodeToString(a.sum("GET\n"+u.EscapedPath()+"\n"+exp)))
	u.RawQuery=q.Encode()
	return u.String(),nil
}

func (a *Auth) ValidDownload(r *http.Request) bool {
	if r.Method != http.MethodGet { return false }
	query := r.URL.Query()
	if len(query["exp"]) != 1 || len(query["sig"]) != 1 { return false }
	exp,err := strconv.ParseInt(query.Get("exp"),10,64)
	if err != nil || strconv.FormatInt(exp,10) != query.Get("exp") || time.Now().Unix() >= exp { return false }
	sig,err := hex.DecodeString(query.Get("sig"))
	return err == nil && hmac.Equal(sig,a.sum("GET\n"+r.URL.EscapedPath()+"\n"+query.Get("exp")))
}
