package build

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/docker/buildx/util/gitutil"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

const DockerfileLabel = "com.docker.image.source.entrypoint"

func getGitAttributes(ctx context.Context, contextPath string, dockerfilePath string) (res map[string]string, _ error) {
	res = make(map[string]string)
	if contextPath == "" {
		return
	}

	setGitLabels := false
	if v, ok := os.LookupEnv("BUILDX_GIT_LABELS"); ok {
		if v == "full" { // backward compatibility with old "full" mode
			setGitLabels = true
		} else if v, err := strconv.ParseBool(v); err == nil {
			setGitLabels = v
		}
	}
	setGitInfo := true
	if v, ok := os.LookupEnv("BUILDX_GIT_INFO"); ok {
		if v, err := strconv.ParseBool(v); err == nil {
			setGitInfo = v
		}
	}

	if !setGitLabels && !setGitInfo {
		return
	}

	// figure out in which directory the git command needs to run in
	var wd string
	if filepath.IsAbs(contextPath) {
		wd = contextPath
	} else {
		wd, _ = filepath.Abs(filepath.Join(getWd(), contextPath))
	}
	wd = gitutil.SanitizePath(wd)

	gitc, err := gitutil.New(gitutil.WithContext(ctx), gitutil.WithWorkingDir(wd))
	if err != nil {
		if st, err1 := os.Stat(path.Join(wd, ".git")); err1 == nil && st.IsDir() {
			return res, errors.Wrap(err, "git was not found in the system")
		}
		return
	}

	if !gitc.IsInsideWorkTree() {
		if st, err := os.Stat(path.Join(wd, ".git")); err == nil && st.IsDir() {
			return res, errors.New("failed to read current commit information with git rev-parse --is-inside-work-tree")
		}
		return res, nil
	}

	if sha, err := gitc.FullCommit(); err != nil && !gitutil.IsUnknownRevision(err) {
		return res, errors.Wrap(err, "failed to get git commit")
	} else if sha != "" {
		checkDirty := false
		if v, ok := os.LookupEnv("BUILDX_GIT_CHECK_DIRTY"); ok {
			if v, err := strconv.ParseBool(v); err == nil {
				checkDirty = v
			}
		}
		if checkDirty && gitc.IsDirty() {
			sha += "-dirty"
		}
		if setGitLabels {
			res["label:"+specs.AnnotationRevision] = sha
		}
		if setGitInfo {
			res["vcs:revision"] = sha
		}
	}

	if rurl, err := gitc.RemoteURL(); err == nil && rurl != "" {
		if setGitLabels {
			res["label:"+specs.AnnotationSource] = rurl
		}
		if setGitInfo {
			res["vcs:source"] = rurl
		}
	}

	if setGitLabels {
		if root, err := gitc.RootDir(); err != nil {
			return res, errors.Wrap(err, "failed to get git root dir")
		} else if root != "" {
			if dockerfilePath == "" {
				dockerfilePath = filepath.Join(wd, "Dockerfile")
			}
			if !filepath.IsAbs(dockerfilePath) {
				dockerfilePath = filepath.Join(getWd(), dockerfilePath)
			}
			dockerfilePath, _ = filepath.Rel(root, dockerfilePath)
			if !strings.HasPrefix(dockerfilePath, "..") {
				res["label:"+DockerfileLabel] = dockerfilePath
			}
		}
	}

	return
}

func getWd() string {
	wd, _ := os.Getwd()
	if lp, err := getLongPathName(wd); err == nil {
		return lp
	}
	return wd
}
