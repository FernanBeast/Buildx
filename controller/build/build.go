package build

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/docker/buildx/build"
	"github.com/docker/buildx/builder"
	controllerapi "github.com/docker/buildx/controller/pb"
	"github.com/docker/buildx/store"
	"github.com/docker/buildx/store/storeutil"
	"github.com/docker/buildx/util/buildflags"
	"github.com/docker/buildx/util/confutil"
	"github.com/docker/buildx/util/dockerutil"
	"github.com/docker/buildx/util/platformutil"
	"github.com/docker/buildx/util/progress"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/config"
	dockeropts "github.com/docker/cli/opts"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/docker/go-units"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session/auth/authprovider"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

const defaultTargetName = "default"

// ProgressConfig is configuration about how the progress information is printed.
type ProgressConfig struct {
	Printer           func(ctx context.Context, ng *store.NodeGroup) (*progress.Printer, error)
	PrintWarningsFunc func(warnings []client.VertexWarning)
	PrintResultFunc   func(f *build.PrintFunc, res map[string]string) error
}

// RunBuild builds the specified build with providing the build status information through ProgressConfig.
func RunBuild(ctx context.Context, dockerCli command.Cli, in controllerapi.BuildOptions, inStream io.Reader, progressConfig ProgressConfig) (*client.SolveResponse, *build.ResultContext, error) {
	if in.NoCache && len(in.NoCacheFilter) > 0 {
		return nil, nil, errors.Errorf("--no-cache and --no-cache-filter cannot currently be used together")
	}

	contexts := map[string]build.NamedContext{}
	for name, path := range in.NamedContexts {
		contexts[name] = build.NamedContext{Path: path}
	}

	printFunc, err := parsePrintFunc(in.PrintFunc)
	if err != nil {
		return nil, nil, err
	}

	opts := build.Options{
		Inputs: build.Inputs{
			ContextPath:    in.ContextPath,
			DockerfilePath: in.DockerfileName,
			InStream:       inStream,
			NamedContexts:  contexts,
		},
		BuildArgs:     in.BuildArgs,
		ExtraHosts:    in.ExtraHosts,
		Labels:        in.Labels,
		NetworkMode:   in.NetworkMode,
		NoCache:       in.NoCache,
		NoCacheFilter: in.NoCacheFilter,
		Pull:          in.Pull,
		ShmSize:       dockeropts.MemBytes(in.ShmSize),
		Tags:          in.Tags,
		Target:        in.Target,
		Ulimits:       controllerUlimitOpt2DockerUlimit(in.Ulimits),
		PrintFunc:     printFunc,
	}

	platforms, err := platformutil.Parse(in.Platforms)
	if err != nil {
		return nil, nil, err
	}
	opts.Platforms = platforms

	dockerConfig := config.LoadDefaultConfigFile(os.Stderr)
	opts.Session = append(opts.Session, authprovider.NewDockerAuthProvider(dockerConfig))

	secrets, err := controllerapi.CreateSecrets(in.Secrets)
	if err != nil {
		return nil, nil, err
	}
	opts.Session = append(opts.Session, secrets)

	sshSpecs := in.SSH
	if len(sshSpecs) == 0 && buildflags.IsGitSSH(in.ContextPath) {
		sshSpecs = append(sshSpecs, &controllerapi.SSH{ID: "default"})
	}
	ssh, err := controllerapi.CreateSSH(sshSpecs)
	if err != nil {
		return nil, nil, err
	}
	opts.Session = append(opts.Session, ssh)

	outputs, err := controllerapi.CreateExports(in.Exports)
	if err != nil {
		return nil, nil, err
	}
	if in.ExportPush {
		if in.ExportLoad {
			return nil, nil, errors.Errorf("push and load may not be set together at the moment")
		}
		if len(outputs) == 0 {
			outputs = []client.ExportEntry{{
				Type: "image",
				Attrs: map[string]string{
					"push": "true",
				},
			}}
		} else {
			switch outputs[0].Type {
			case "image":
				outputs[0].Attrs["push"] = "true"
			default:
				return nil, nil, errors.Errorf("push and %q output can't be used together", outputs[0].Type)
			}
		}
	}
	if in.ExportLoad {
		if len(outputs) == 0 {
			outputs = []client.ExportEntry{{
				Type:  "docker",
				Attrs: map[string]string{},
			}}
		} else {
			switch outputs[0].Type {
			case "docker":
			default:
				return nil, nil, errors.Errorf("load and %q output can't be used together", outputs[0].Type)
			}
		}
	}
	opts.Exports = outputs

	opts.CacheFrom = controllerapi.CreateCaches(in.CacheFrom)
	opts.CacheTo = controllerapi.CreateCaches(in.CacheTo)

	opts.Attests = controllerapi.CreateAttestations(in.Attests)

	allow, err := buildflags.ParseEntitlements(in.Allow)
	if err != nil {
		return nil, nil, err
	}
	opts.Allow = allow

	// key string used for kubernetes "sticky" mode
	contextPathHash, err := filepath.Abs(in.ContextPath)
	if err != nil {
		contextPathHash = in.ContextPath
	}

	b, err := builder.New(dockerCli,
		builder.WithName(in.Builder),
		builder.WithContextPathHash(contextPathHash),
	)
	if err != nil {
		return nil, nil, err
	}
	if err = updateLastActivity(dockerCli, b.NodeGroup); err != nil {
		return nil, nil, errors.Wrapf(err, "failed to update builder last activity time")
	}
	nodes, err := b.LoadNodes(ctx, false)
	if err != nil {
		return nil, nil, err
	}

	resp, res, err := buildTargets(ctx, dockerCli, b.NodeGroup, nodes, map[string]build.Options{defaultTargetName: opts}, progressConfig, in.MetadataFile)
	err = wrapBuildError(err, false)
	if err != nil {
		return nil, nil, err
	}
	return resp, res, nil
}

func buildTargets(ctx context.Context, dockerCli command.Cli, ng *store.NodeGroup, nodes []builder.Node, opts map[string]build.Options, progressConfig ProgressConfig, metadataFile string) (*client.SolveResponse, *build.ResultContext, error) {
	ctx2, cancel := context.WithCancel(context.TODO())
	defer cancel()

	printer, err := progressConfig.Printer(ctx2, ng)
	if err != nil {
		return nil, nil, err
	}

	var res *build.ResultContext
	var mu sync.Mutex
	var idx int
	resp, err := build.BuildWithResultHandler(ctx, nodes, opts, dockerutil.NewClient(dockerCli), confutil.ConfigDir(dockerCli), printer, func(driverIndex int, gotRes *build.ResultContext) {
		mu.Lock()
		defer mu.Unlock()
		if res == nil || driverIndex < idx {
			idx, res = driverIndex, gotRes
		}
	})
	err1 := printer.Wait()
	if err == nil {
		err = err1
	}
	if err != nil {
		return nil, nil, err
	}

	if len(metadataFile) > 0 && resp != nil {
		if err := writeMetadataFile(metadataFile, decodeExporterResponse(resp[defaultTargetName].ExporterResponse)); err != nil {
			return nil, nil, err
		}
	}

	if f := progressConfig.PrintWarningsFunc; f != nil {
		f(printer.Warnings())
	}

	for k := range resp {
		if opts[k].PrintFunc != nil {
			if err := progressConfig.PrintResultFunc(opts[k].PrintFunc, resp[k].ExporterResponse); err != nil {
				return nil, nil, err
			}
		}
	}

	return resp[defaultTargetName], res, err
}

func parsePrintFunc(str string) (*build.PrintFunc, error) {
	if str == "" {
		return nil, nil
	}
	csvReader := csv.NewReader(strings.NewReader(str))
	fields, err := csvReader.Read()
	if err != nil {
		return nil, err
	}
	f := &build.PrintFunc{}
	for _, field := range fields {
		parts := strings.SplitN(field, "=", 2)
		if len(parts) == 2 {
			if parts[0] == "format" {
				f.Format = parts[1]
			} else {
				return nil, errors.Errorf("invalid print field: %s", field)
			}
		} else {
			if f.Name != "" {
				return nil, errors.Errorf("invalid print value: %s", str)
			}
			f.Name = field
		}
	}
	return f, nil
}

func writeMetadataFile(filename string, dt interface{}) error {
	b, err := json.MarshalIndent(dt, "", "  ")
	if err != nil {
		return err
	}
	return ioutils.AtomicWriteFile(filename, b, 0644)
}

func decodeExporterResponse(exporterResponse map[string]string) map[string]interface{} {
	out := make(map[string]interface{})
	for k, v := range exporterResponse {
		dt, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			out[k] = v
			continue
		}
		var raw map[string]interface{}
		if err = json.Unmarshal(dt, &raw); err != nil || len(raw) == 0 {
			out[k] = v
			continue
		}
		out[k] = json.RawMessage(dt)
	}
	return out
}

func wrapBuildError(err error, bake bool) error {
	if err == nil {
		return nil
	}
	st, ok := grpcerrors.AsGRPCStatus(err)
	if ok {
		if st.Code() == codes.Unimplemented && strings.Contains(st.Message(), "unsupported frontend capability moby.buildkit.frontend.contexts") {
			msg := "current frontend does not support --build-context."
			if bake {
				msg = "current frontend does not support defining additional contexts for targets."
			}
			msg += " Named contexts are supported since Dockerfile v1.4. Use #syntax directive in Dockerfile or update to latest BuildKit."
			return &wrapped{err, msg}
		}
	}
	return err
}

type wrapped struct {
	err error
	msg string
}

func (w *wrapped) Error() string {
	return w.msg
}

func (w *wrapped) Unwrap() error {
	return w.err
}

func updateLastActivity(dockerCli command.Cli, ng *store.NodeGroup) error {
	txn, release, err := storeutil.GetStore(dockerCli)
	if err != nil {
		return err
	}
	defer release()
	return txn.UpdateLastActivity(ng)
}

func controllerUlimitOpt2DockerUlimit(u *controllerapi.UlimitOpt) *dockeropts.UlimitOpt {
	if u == nil {
		return nil
	}
	values := make(map[string]*units.Ulimit)
	for k, v := range u.Values {
		values[k] = &units.Ulimit{
			Name: v.Name,
			Hard: v.Hard,
			Soft: v.Soft,
		}
	}
	return dockeropts.NewUlimitOpt(&values)
}
