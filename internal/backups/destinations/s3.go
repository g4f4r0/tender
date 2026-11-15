package destinations

import (
	"net/url"

	"github.com/g4f4r0/tender/internal/server"
)

// s3 is any S3-compatible bucket: AWS, Cloudflare R2, Backblaze B2, Wasabi, MinIO, and Google
// Cloud Storage through its S3 interoperability.
type s3 struct{}

type s3Config struct {
	Endpoint        string `json:"endpoint"`
	Bucket          string `json:"bucket"`
	Path            string `json:"path"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

func (s3) Fields() []string {
	return []string{"endpoint", "bucket", "path", "access_key_id", "secret_access_key"}
}

func (s3) Secrets() []string { return []string{"access_key_id", "secret_access_key"} }

func (t s3) Check(c Config) (Config, error) {
	var v s3Config
	if err := decode("s3", t, c, &v); err != nil {
		return nil, err
	}
	v.Path = trimPath(v.Path)
	u, err := url.Parse(v.Endpoint)
	switch {
	case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil:
		return nil, server.Invalid("config.endpoint must be a URL such as https://<account>.r2.cloudflarestorage.com.")
	case v.Bucket == "":
		return nil, required("bucket", "the bucket's name")
	case v.AccessKeyID == "":
		return nil, required("access_key_id", "the bucket's access key ID")
	case v.SecretAccessKey == "":
		return nil, required("secret_access_key", "the bucket's secret access key")
	}
	if err := cleanPath("path", v.Path); err != nil {
		return nil, err
	}
	return encode(v), nil
}

func (s3) Location(c Config) string {
	repo := "s3:" + str(c, "endpoint") + "/" + str(c, "bucket")
	if p := str(c, "path"); p != "" {
		repo += "/" + p
	}
	return repo
}

func (t s3) Open(c Config, machine string, _ Host) (Repo, error) {
	return Repo{
		Repository: t.Location(c) + "/" + machine,
		Env:        []string{"AWS_ACCESS_KEY_ID=" + str(c, "access_key_id"), "AWS_SECRET_ACCESS_KEY=" + str(c, "secret_access_key")},
	}, nil
}

// str is a string field of c, or "".
func str(c Config, field string) string {
	s, _ := c[field].(string)
	return s
}
