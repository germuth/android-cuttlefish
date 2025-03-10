// Copyright 2022 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/android-cuttlefish/frontend/src/host_orchestrator/orchestrator/artifacts"
	"github.com/google/android-cuttlefish/frontend/src/host_orchestrator/orchestrator/cvd"
	apiv1 "github.com/google/android-cuttlefish/frontend/src/liboperator/api/v1"
	"github.com/google/android-cuttlefish/frontend/src/liboperator/operator"

	"github.com/hashicorp/go-multierror"
)

type ExecContext = func(ctx context.Context, name string, arg ...string) *exec.Cmd

type Validator interface {
	Validate() error
}

type EmptyFieldError string

func (s EmptyFieldError) Error() string {
	return fmt.Sprintf("field %v is empty", string(s))
}

type AndroidBuild struct {
	ID     string
	Target string
}

type IMPaths struct {
	RootDir          string
	CVDToolsDir      string
	ArtifactsRootDir string
	RuntimesRootDir  string
}

func (p *IMPaths) CVDBin() string {
	return filepath.Join(p.CVDToolsDir, "cvd")
}

func (p *IMPaths) FetchCVDBin() string {
	return filepath.Join(p.CVDToolsDir, "fetch_cvd")
}

// Creates a CVD execution context from a regular execution context.
// If a non-empty user name is provided the returned execution context executes commands as that user.
func newCVDExecContext(execContext ExecContext, user string) cvd.CVDExecContext {
	if user != "" {
		return func(ctx context.Context, env []string, name string, arg ...string) *exec.Cmd {
			newArgs := []string{"-u", user}
			if env != nil {
				newArgs = append(newArgs, env...)
			}
			newArgs = append(newArgs, name)
			newArgs = append(newArgs, arg...)
			return execContext(ctx, "sudo", newArgs...)
		}
	}
	return func(ctx context.Context, env []string, name string, arg ...string) *exec.Cmd {
		cmd := execContext(ctx, name, arg...)
		if cmd != nil {
			cmd.Env = env
		}
		return cmd
	}
}

// Makes runtime artifacts owned by `cvdnetwork` group.
func createRuntimesRootDir(name string) error {
	if err := createDir(name); err != nil {
		return err
	}
	return os.Chmod(name, 0774|os.ModeSetgid)
}

type cvdFleetOutput struct {
	Groups []*cvdGroup `json:"groups"`
}

type cvdGroup struct {
	Name      string         `json:"group_name"`
	Instances []*cvdInstance `json:"instances"`
}

type cvdInstance struct {
	InstanceName   string   `json:"instance_name"`
	Status         string   `json:"status"`
	Displays       []string `json:"displays"`
	InstanceDir    string   `json:"instance_dir"`
	WebRTCDeviceID string   `json:"webrtc_device_id"`
}

func cvdFleet(ctx cvd.CVDExecContext, cvdBin string) ([]*cvdInstance, error) {
	stdout := &bytes.Buffer{}
	cvdCmd := cvd.NewCommand(ctx, cvdBin, []string{"fleet"}, cvd.CommandOpts{Stdout: stdout})
	err := cvdCmd.Run()
	if err != nil {
		return nil, err
	}
	output := &cvdFleetOutput{}
	if err := json.Unmarshal(stdout.Bytes(), output); err != nil {
		log.Printf("Failed parsing `cvd fleet` ouput. Output: \n\n%s\n", cvd.OutputLogMessage(stdout.String()))
		return nil, fmt.Errorf("failed parsing `cvd fleet` output: %w", err)
	}
	if len(output.Groups) == 0 {
		return []*cvdInstance{}, nil
	}
	// Host orchestrator only works with one instances group.
	return output.Groups[0].Instances, nil
}

func fleetToCVDs(val []*cvdInstance) []*apiv1.CVD {
	result := make([]*apiv1.CVD, len(val))
	for i, item := range val {
		result[i] = &apiv1.CVD{
			Name: item.InstanceName,
			// TODO(b/259725479): Update when `cvd fleet` prints out build information.
			BuildSource:    &apiv1.BuildSource{},
			Status:         item.Status,
			Displays:       item.Displays,
			WebRTCDeviceID: item.WebRTCDeviceID,
		}
	}
	return result
}

func CVDLogsDir(ctx cvd.CVDExecContext, cvdBin, name string) (string, error) {
	instances, err := cvdFleet(ctx, cvdBin)
	if err != nil {
		return "", err
	}
	ok, ins := cvdInstances(instances).findByName(name)
	if !ok {
		return "", operator.NewNotFoundError(fmt.Sprintf("Instance %q not found", name), nil)
	}
	return ins.InstanceDir + "/logs", nil
}

func HostBugReport(ctx cvd.CVDExecContext, paths IMPaths, out string) error {
	fleet, err := cvdFleet(ctx, paths.CVDBin())
	if err != nil {
		return err
	}
	if len(fleet) == 0 {
		return operator.NewNotFoundError("no artifacts found", nil)
	}
	opts := cvd.CommandOpts{
		Home: paths.RuntimesRootDir,
	}
	return cvd.NewCommand(ctx, paths.CVDBin(), []string{"host_bugreport", "--output=" + out}, opts).Run()
}

const (
	// TODO(b/267525748): Make these values configurable.
	mainBuildDefaultBranch = "aosp-main"
	mainBuildDefaultTarget = "aosp_cf_x86_64_phone-trunk_staging-userdebug"
)

func defaultMainBuild() *apiv1.AndroidCIBuild {
	return &apiv1.AndroidCIBuild{Branch: mainBuildDefaultBranch, Target: mainBuildDefaultTarget}
}

const CVDHostPackageName = "cvd-host_package.tar.gz"

func untarCVDHostPackage(dir string) error {
	if err := Untar(dir, dir+"/"+CVDHostPackageName); err != nil {
		return fmt.Errorf("Failed to untar %s with error: %w", CVDHostPackageName, err)
	}
	return nil
}

type CVDDownloader interface {
	// Downloads the `cvd` and `fetch_cvd` binaries into the given filenames.
	Download(build AndroidBuild, outCVD, outFetchCVD string) error
}

type AndroidCICVDDownloader struct {
	buildAPI artifacts.BuildAPI
}

func NewAndroidCICVDDownloader(buildAPI artifacts.BuildAPI) *AndroidCICVDDownloader {
	return &AndroidCICVDDownloader{
		buildAPI: buildAPI,
	}
}

func (h *AndroidCICVDDownloader) Download(build AndroidBuild, outCVD, outFetchCVD string) error {
	if err := h.download(build, outCVD); err != nil {
		return fmt.Errorf("failed downloading cvd file: %w", err)
	}
	if err := h.download(build, outFetchCVD); err != nil {
		return fmt.Errorf("failed downloading fetch_cvd file: %w", err)
	}
	return nil
}

func (h *AndroidCICVDDownloader) download(build AndroidBuild, out string) error {
	exist, err := fileExist(out)
	if err != nil {
		return fmt.Errorf("failed to test if the `%s` file %q does exist: %w", filepath.Base(out), out, err)
	}
	if exist {
		return nil
	}
	f, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("failed to create the `%s` file %q: %w", filepath.Base(out), out, err)
	}
	var downloadErr error
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("failed closing `%s` file %q file, error: %v", filepath.Base(out), out, err)
		}
		if downloadErr != nil {
			if err := os.Remove(out); err != nil {
				log.Printf("failed removing  `%s` file %q: %v", filepath.Base(out), out, err)
			}
		}

	}()
	if err := h.buildAPI.DownloadArtifact(filepath.Base(out), build.ID, build.Target, f); err != nil {
		return err
	}
	return os.Chmod(out, 0750)
}

type fetchCVDCommandArtifactsFetcher struct {
	execContext ExecContext
	fetchCVDBin string
	credentials string
}

func newFetchCVDCommandArtifactsFetcher(
	execContext ExecContext,
	fetchCVDBin string,
	credentials string) *fetchCVDCommandArtifactsFetcher {
	return &fetchCVDCommandArtifactsFetcher{
		execContext: execContext,
		fetchCVDBin: fetchCVDBin,
		credentials: credentials,
	}
}

// The artifacts directory gets created during the execution of `fetch_cvd` granting access to the cvdnetwork group
// which translated to granting the necessary permissions to the cvd executor user.
func (f *fetchCVDCommandArtifactsFetcher) Fetch(outDir, buildID, target string, extraOptions *artifacts.ExtraCVDOptions) error {
	args := []string{
		fmt.Sprintf("--directory=%s", outDir),
		fmt.Sprintf("--default_build=%s/%s", buildID, target),
	}
	if extraOptions != nil {
		args = append(args,
			fmt.Sprintf("--system_build=%s/%s", extraOptions.SystemImgBuildID, extraOptions.SystemImgTarget))
	}
	var file *os.File
	var err error
	fetchCmd := f.execContext(context.TODO(), f.fetchCVDBin, args...)
	if f.credentials != "" {
		if file, err = createCredentialsFile(f.credentials); err != nil {
			return err
		}
		defer file.Close()
		// This is necessary for the subprocess to inherit the file.
		fetchCmd.ExtraFiles = append(fetchCmd.ExtraFiles, file)
		// The actual fd number is not retained, the lowest available number is used instead.
		fd := 3 + len(fetchCmd.ExtraFiles) - 1
		fetchCmd.Args = append(fetchCmd.Args, fmt.Sprintf("--credential_source=/proc/self/fd/%d", fd))
	}
	out, err := fetchCmd.CombinedOutput()
	if err != nil {
		cvd.LogCombinedStdoutStderr(fetchCmd, string(out))
		return fmt.Errorf("`fetch_cvd` failed: %w. \n\nCombined Output:\n%s", err, string(out))
	}
	// TODO(b/286466643): Remove this hack once cuttlefish is capable of booting from read-only artifacts again.
	chmodCmd := f.execContext(context.TODO(), "chmod", "-R", "g+rw", outDir)
	chmodOut, err := chmodCmd.CombinedOutput()
	if err != nil {
		cvd.LogCombinedStdoutStderr(chmodCmd, string(chmodOut))
		return err
	}
	return nil
}

func createCredentialsFile(content string) (*os.File, error) {
	p1, p2, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("Failed to create pipe for credentials: %w", err)
	}
	go func(f *os.File) {
		defer f.Close()
		if _, err := f.Write([]byte(content)); err != nil {
			log.Printf("Failed to write credentials to file: %v\n", err)
			// Can't return this error without risking a deadlock when the pipe buffer fills up.
		}
	}(p2)
	return p1, nil
}

type buildAPIArtifacstFetcher struct {
	buildAPI artifacts.BuildAPI
}

func newBuildAPIArtifactsFetcher(buildAPI artifacts.BuildAPI) *buildAPIArtifacstFetcher {
	return &buildAPIArtifacstFetcher{
		buildAPI: buildAPI,
	}
}

func (f *buildAPIArtifacstFetcher) Fetch(outDir, buildID, target string, artifactNames ...string) error {
	var chans []chan error
	for _, name := range artifactNames {
		ch := make(chan error)
		chans = append(chans, ch)
		go func(name string) {
			defer close(ch)
			filename := outDir + "/" + name
			if err := downloadArtifactToFile(f.buildAPI, filename, name, buildID, target); err != nil {
				ch <- err
			}
		}(name)
	}
	var merr error
	for _, ch := range chans {
		for err := range ch {
			merr = multierror.Append(merr, err)
		}
	}
	return merr
}

const (
	daemonArg = "--daemon"
	// TODO(b/242599859): Add report_anonymous_usage_stats as a parameter to the Create CVD API.
	reportAnonymousUsageStatsArg = "--report_anonymous_usage_stats=y"
	groupNameArg                 = "--group_name=cvd"
)

type startCVDHandler struct {
	ExecContext cvd.CVDExecContext
	CVDBin      string
	Timeout     time.Duration
}

type startCVDParams struct {
	InstanceNumbers  []uint32
	MainArtifactsDir string
	RuntimeDir       string
	// OPTIONAL. If set, kernel relevant artifacts will be pulled from this dir.
	KernelDir string
	// OPTIONAL. If set, bootloader relevant artifacts will be pulled from this dir.
	BootloaderDir string
}

func (h *startCVDHandler) Start(p startCVDParams) error {
	args := []string{groupNameArg, "start", daemonArg, reportAnonymousUsageStatsArg}
	if len(p.InstanceNumbers) == 1 {
		// Use legacy `--base_instance_num` when multi-vd is not requested.
		args = append(args, fmt.Sprintf("--base_instance_num=%d", p.InstanceNumbers[0]))
	} else {
		args = append(args, fmt.Sprintf("--num_instances=%s", strings.Join(SliceItoa(p.InstanceNumbers), ",")))
	}
	args = append(args, fmt.Sprintf("--system_image_dir=%s", p.MainArtifactsDir))
	if len(p.InstanceNumbers) > 1 {
		args = append(args, fmt.Sprintf("--num_instances=%d", len(p.InstanceNumbers)))
	}
	if p.KernelDir != "" {
		args = append(args, fmt.Sprintf("--kernel_path=%s/bzImage", p.KernelDir))
		initramfs := filepath.Join(p.KernelDir, "initramfs.img")
		if exist, _ := fileExist(initramfs); exist {
			args = append(args, "--initramfs_path="+initramfs)
		}
	}
	if p.BootloaderDir != "" {
		args = append(args, fmt.Sprintf("--bootloader=%s/u-boot.rom", p.BootloaderDir))
	}
	opts := cvd.CommandOpts{
		AndroidHostOut: p.MainArtifactsDir,
		Home:           p.RuntimeDir,
		Timeout:        h.Timeout,
	}
	cvdCmd := cvd.NewCommand(h.ExecContext, h.CVDBin, args, opts)
	err := cvdCmd.Run()
	if err != nil {
		return fmt.Errorf("launch cvd stage failed: %w", err)
	}
	return nil
}

// Fails if the directory already exists.
func createNewDir(dir string) error {
	err := os.Mkdir(dir, 0774)
	if err != nil {
		return err
	}
	// Sets dir permission regardless of umask.
	return os.Chmod(dir, 0774)
}

func createDir(dir string) error {
	if err := createNewDir(dir); os.IsExist(err) {
		return nil
	} else {
		return err
	}
}

func fileExist(name string) (bool, error) {
	if _, err := os.Stat(name); err == nil {
		return true, nil
	} else if os.IsNotExist(err) {
		return false, nil
	} else {
		return false, err
	}
}

// Validates whether the current host is valid to run CVDs.
type HostValidator struct {
	ExecContext ExecContext
}

func (v *HostValidator) Validate() error {
	if ok, _ := fileExist("/dev/kvm"); !ok {
		return operator.NewInternalError("Nested virtualization is not enabled.", nil)
	}
	return nil
}

// Helper to update the passed builds with latest green BuildID if build is not nil and BuildId is empty.
func updateBuildsWithLatestGreenBuildID(buildAPI artifacts.BuildAPI, builds []*apiv1.AndroidCIBuild) error {
	var chans []chan error
	for _, build := range builds {
		ch := make(chan error)
		chans = append(chans, ch)
		go func(build *apiv1.AndroidCIBuild) {
			defer close(ch)
			if build != nil && build.BuildID == "" {
				if err := updateBuildWithLatestGreenBuildID(buildAPI, build); err != nil {
					ch <- err
				}
			}
		}(build)
	}
	var merr error
	for _, ch := range chans {
		for err := range ch {
			merr = multierror.Append(merr, err)
		}
	}
	return merr
}

// Helper to update the passed `build` with latest green BuildID.
func updateBuildWithLatestGreenBuildID(buildAPI artifacts.BuildAPI, build *apiv1.AndroidCIBuild) error {
	buildID, err := buildAPI.GetLatestGreenBuildID(build.Branch, build.Target)
	if err != nil {
		return err
	}
	build.BuildID = buildID
	return nil
}

// Download artifacts helper. Fails if file already exists.
func downloadArtifactToFile(buildAPI artifacts.BuildAPI, filename, artifactName, buildID, target string) error {
	exist, err := fileExist(target)
	if err != nil {
		return fmt.Errorf("download artifact %q failed: %w", filename, err)
	}
	if exist {
		return fmt.Errorf("download artifact %q failed: file already exists", filename)
	}
	f, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("download artifact %q failed: %w", filename, err)
	}
	var downloadErr error
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("download artifact: failed closing %q, error: %v", filename, err)
		}
		if downloadErr != nil {
			if err := os.Remove(filename); err != nil {
				log.Printf("download artifact: failed removing %q: %v", filename, err)
			}
		}
	}()
	downloadErr = buildAPI.DownloadArtifact(artifactName, buildID, target, f)
	return downloadErr
}

type cvdInstances []*cvdInstance

func (s cvdInstances) findByName(name string) (bool, *cvdInstance) {
	for _, e := range s {
		if e.InstanceName == name {
			return true, e
		}
	}
	return false, &cvdInstance{}
}

func runAcloudSetup(execContext cvd.CVDExecContext, artifactsRootDir, artifactsDir, runtimeDir string) {
	run := func(cmd *exec.Cmd) {
		var b bytes.Buffer
		cmd.Stdout = &b
		cmd.Stderr = &b
		err := cmd.Run()
		if err != nil {
			log.Println("runAcloudSetup failed with error: " + b.String())
		}
	}
	// Creates symbolic link `acloud_link` which points to the passed device artifacts directory.
	go run(execContext(context.TODO(), nil, "ln", "-s", artifactsDir, artifactsRootDir+"/acloud_link"))
}

func SliceItoa(s []uint32) []string {
	result := make([]string, len(s))
	for i, v := range s {
		result[i] = strconv.Itoa(int(v))
	}
	return result
}

func contains(s []uint32, e uint32) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}
