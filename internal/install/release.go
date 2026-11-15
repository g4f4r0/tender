package install

import (
	"bytes"
	"cmp"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Where Tender's releases are. EnvTestGitHub replaces both hosts, for the end-to-end test's fake
// GitHub only: the release's signature still has to check out against an embedded key.
const (
	releasesAPI = "https://api.github.com/repos/g4f4r0/tender/releases"
	downloadURL = "https://github.com/g4f4r0/tender/releases/download"

	EnvTestGitHub = "TENDER_TEST_GITHUB"

	releaseAsset    = "tender_linux_amd64"
	checksumsAsset  = "SHA256SUMS"
	SignatureAsset  = "SHA256SUMS.sig"
	maxReleaseBytes = 128 << 20
	maxSumsBytes    = 1 << 20

	issuesURL = "https://github.com/g4f4r0/tender/issues"
)

// ReleaseURLs returns the GitHub API URL of Tender's releases and the URL their files download from.
func ReleaseURLs(getenv func(string) string) (api, download string) {
	if base := strings.TrimRight(getenv(EnvTestGitHub), "/"); base != "" {
		return base + "/repos/g4f4r0/tender/releases", base + "/g4f4r0/tender/releases/download"
	}
	return releasesAPI, downloadURL
}

// releaseKeys are the public keys that sign Tender's releases: base64 of 32-byte ed25519 keys. The
// release workflow signs each release's SHA256SUMS with the private key, which lives only in a
// GitHub Actions secret. It is a list, so a new key can ship before the old one is retired.
var releaseKeys = []string{"FFCQetX09NSfXliwZi1eVws0iGQkNwZch0dLQTru3TI="}

// testReleaseKey is one more key, set only in the end-to-end test's builds with
// -ldflags "-X github.com/g4f4r0/tender/internal/install.testReleaseKey=...". Release builds leave it empty.
var testReleaseKey string

// ReleaseKeys returns the keys a release's signature may come from.
func ReleaseKeys() []ed25519.PublicKey {
	var out []ed25519.PublicKey
	for _, k := range append(releaseKeys, testReleaseKey) {
		if raw, err := base64.StdEncoding.DecodeString(k); err == nil && len(raw) == ed25519.PublicKeySize {
			out = append(out, ed25519.PublicKey(raw))
		}
	}
	return out
}

// verifySums checks that sig, the base64 text of SHA256SUMS.sig, is an ed25519 signature of sums
// by one of keys.
func verifySums(keys []ed25519.PublicKey, sums, sig []byte) bool {
	raw, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return false
	}
	for _, k := range keys {
		if ed25519.Verify(k, sums, raw) {
			return true
		}
	}
	return false
}

// Release is a GitHub release, as its API describes it.
type Release struct {
	Tag        string  `json:"tag_name"`
	Prerelease bool    `json:"prerelease"`
	Draft      bool    `json:"draft"`
	Assets     []Asset `json:"assets"`
}

// Asset is one file of a release.
type Asset struct {
	Name string `json:"name"`
}

// HasAsset reports whether the release has a file called name.
func (r Release) HasAsset(name string) bool {
	for _, a := range r.Assets {
		if a.Name == name {
			return true
		}
	}
	return false
}

// GetRelease asks GitHub about the release with tag, or about the latest when tag is "".
func GetRelease(ctx context.Context, api, tag string) (Release, error) {
	url := api + "/latest"
	if tag != "" {
		if !releaseTag.MatchString(tag) {
			return Release{}, fmt.Errorf("%q is not a Tender version. Use a release such as v0.3.0", tag)
		}
		url = api + "/tags/" + tag
	}
	body, err := get(ctx, url, maxSumsBytes)
	if err != nil {
		return Release{}, err
	}
	var r Release
	if err := json.Unmarshal(body, &r); err != nil || !releaseTag.MatchString(r.Tag) {
		return Release{}, errors.New("GitHub did not name a Tender release")
	}
	return r, nil
}

// Official is nil for an official stable release, and otherwise says why the release is refused.
func (r Release) Official() error {
	switch {
	case r.Draft:
		return fmt.Errorf("Tender %s is a draft, not a published release", r.Tag)
	case r.Prerelease || !stableTag.MatchString(r.Tag):
		return fmt.Errorf("Tender %s is a prerelease, and Tender only updates to stable releases", r.Tag)
	}
	return nil
}

// releaseTag is the shape of a tender release tag. A tag goes into a URL, so nothing else passes.
var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

// stableTag is a release tag without a prerelease part.
var stableTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// Tag returns version as a release tag, such as v0.3.1 for 0.3.1, or an error when it isn't one.
func Tag(version string) (string, error) {
	tag := version
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	if !releaseTag.MatchString(tag) {
		return "", fmt.Errorf("%q is not a Tender version. Use a release such as v0.3.0", version)
	}
	return tag, nil
}

// version is a parsed Tender version. pre is what follows the first "-", such as rc.1, or the
// "41-g5873c03-dirty" of a build between releases.
type version struct {
	major, minor, patch int
	pre                 string
}

var versionPattern = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:-([0-9A-Za-z.-]+))?$`)

func parseVersion(s string) (version, bool) {
	m := versionPattern.FindStringSubmatch(s)
	if m == nil {
		return version{}, false
	}
	var v version
	var errs [3]error
	v.major, errs[0] = strconv.Atoi(m[1])
	v.minor, errs[1] = strconv.Atoi(m[2])
	v.patch, errs[2] = strconv.Atoi(m[3])
	v.pre = m[4]
	return v, errors.Join(errs[:]...) == nil
}

// CompareVersions returns -1, 0 or 1 as a is older than, the same as or newer than b, as semver
// orders them: a version with a prerelease part is older than the same version without one, and
// prerelease identifiers compare one by one, numbers by value. A version that isn't one, such as
// "dev", is older than every version.
func CompareVersions(a, b string) int {
	va, oka := parseVersion(a)
	vb, okb := parseVersion(b)
	switch {
	case !oka && !okb:
		return 0
	case !oka:
		return -1
	case !okb:
		return 1
	}
	if c := cmp.Compare(va.major, vb.major); c != 0 {
		return c
	}
	if c := cmp.Compare(va.minor, vb.minor); c != 0 {
		return c
	}
	if c := cmp.Compare(va.patch, vb.patch); c != 0 {
		return c
	}
	switch {
	case va.pre == vb.pre:
		return 0
	case va.pre == "":
		return 1
	case vb.pre == "":
		return -1
	}
	return comparePre(strings.Split(va.pre, "."), strings.Split(vb.pre, "."))
}

// comparePre compares prerelease identifiers as semver does: numbers by value and below words,
// words in ASCII order, and a shorter list first when the longer one starts with it.
func comparePre(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		na, erra := strconv.ParseUint(a[i], 10, 64)
		nb, errb := strconv.ParseUint(b[i], 10, 64)
		var c int
		switch {
		case erra == nil && errb == nil:
			c = cmp.Compare(na, nb)
		case erra == nil:
			c = -1
		case errb == nil:
			c = 1
		default:
			c = strings.Compare(a[i], b[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a), len(b))
}

// devPattern is the prerelease part of a build between releases, as git describe names it: the
// commits since the tag and the commit, such as 4-gabc123, or anything -dirty.
var devPattern = regexp.MustCompile(`^[0-9]+-g[0-9a-f]+(-dirty)?$|(^|-)dirty$`)

// DevBuild reports whether v is a development build, such as dev, 0.3.1-4-gabc123 or
// 0.3.1-dirty. It may be newer than the latest release, so the hub never updates it and an
// update job refuses it; root can still update it by hand.
func DevBuild(v string) bool {
	pv, ok := parseVersion(v)
	return !ok || devPattern.MatchString(pv.pre)
}

// ValidVersion reports whether s is a version a daemon may report, such as 0.3.1 or 0.3.1-4-gabc123.
func ValidVersion(s string) bool {
	_, ok := parseVersion(s)
	return ok && len(s) <= 64
}

// ReleaseOptions is the optional asset tender-release.json of a release, for a release that needs a
// person: {"auto": false} keeps auto mode from rolling it out.
const ReleaseOptions = "tender-release.json"

// AutoAllowed reports whether auto mode may roll release r out: true unless its tender-release.json
// says {"auto": false}. A file that can't be read or understood keeps auto mode away too.
func AutoAllowed(ctx context.Context, download string, r Release) (bool, error) {
	if !r.HasAsset(ReleaseOptions) {
		return true, nil
	}
	body, err := get(ctx, download+"/"+r.Tag+"/"+ReleaseOptions, maxSumsBytes)
	if err != nil {
		return false, err
	}
	var opts struct {
		Auto *bool `json:"auto"`
	}
	if err := json.Unmarshal(body, &opts); err != nil {
		return false, fmt.Errorf("%s of %s is not valid JSON: %w", ReleaseOptions, r.Tag, err)
	}
	return opts.Auto == nil || *opts.Auto, nil
}
