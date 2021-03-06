/*
Copyright 2018 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pack

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/gravitational/gravity/lib/archive"
	"github.com/gravitational/gravity/lib/defaults"
	"github.com/gravitational/gravity/lib/loc"
	"github.com/gravitational/gravity/lib/state"
	"github.com/gravitational/gravity/lib/storage"

	dockerarchive "github.com/docker/docker/pkg/archive"
	"github.com/gravitational/trace"
	log "github.com/sirupsen/logrus"
)

// PackagePath generates a path to the package composed of base directory,
// repository name, package name and version
func PackagePath(baseDir string, loc loc.Locator) string {
	// the path can not be too long because it leads to problems like this:
	// https: //github.com/golang/go/issues/6895
	return filepath.Join(baseDir, loc.Repository, loc.Name, loc.Version)
}

// IsUnpacked checks if the package has been unpacked at the provided directory
// (currently just by checking if the dir exists)
func IsUnpacked(targetDir string) (bool, error) {
	info, err := os.Stat(targetDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, trace.Wrap(err)
	}
	if !info.IsDir() {
		return false, trace.Errorf(
			"expected %v to be a directory, got %v", targetDir, info)
	}
	return true, nil
}

// Unpack reads the package from the package service and unpacks its contents
// to base directory targetDir
func Unpack(p PackageService, loc loc.Locator, targetDir string, opts *dockerarchive.TarOptions) error {
	var err error
	// if target dir is not provided, unpack to the default location
	if targetDir == "" {
		stateDir, err := state.GetStateDir()
		if err != nil {
			return trace.Wrap(err)
		}
		baseDir := filepath.Join(stateDir, defaults.LocalDir,
			defaults.PackagesDir, defaults.UnpackedDir)
		targetDir = PackagePath(baseDir, loc)
		log.Infof("Unpacking %v into the default directory %v.",
			loc, targetDir)
	}
	if err := os.MkdirAll(targetDir, defaults.SharedDirMask); err != nil {
		return trace.Wrap(err)
	}
	_, reader, err := p.ReadPackage(loc)
	if err != nil {
		return trace.Wrap(err)
	}
	defer reader.Close()

	if opts == nil {
		opts = archive.DefaultOptions()
	}

	if err := dockerarchive.Untar(reader, targetDir, opts); err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// UnpackIfNotUnpacked unpacks the specified package only if it's not yet unpacked
func UnpackIfNotUnpacked(p PackageService, loc loc.Locator, targetDir string, opts *dockerarchive.TarOptions) error {
	isUnpacked, err := IsUnpacked(targetDir)
	if err != nil {
		return trace.Wrap(err)
	}

	if isUnpacked {
		return nil
	}

	err = Unpack(p, loc, targetDir, opts)
	if err != nil {
		return trace.Wrap(err)
	}

	return nil
}

// GetConfigPackage creates the config package without saving it into package service
func GetConfigPackage(p PackageService, loc loc.Locator, confLoc loc.Locator, args []string) (io.Reader, error) {
	_, reader, err := p.ReadPackage(loc)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	decompressed, err := dockerarchive.DecompressStream(reader)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	tarball := tar.NewReader(decompressed)

	manifest, err := ReadManifest(tarball)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	log.Infof("got manifest: %#v", manifest)
	if manifest.Config == nil {
		return nil, trace.BadParameter("manifest does not have configuration parameters")
	}

	if err := manifest.Config.ParseArgs(args); err != nil {
		log.Warnf("Failed to parse arguments: %v.", err)
		return nil, trace.Wrap(err)
	}

	// now create a new package with configuration inside
	buf := &bytes.Buffer{}
	if err := WriteConfigPackage(manifest, buf); err != nil {
		return nil, trace.Wrap(err)
	}

	return buf, nil
}

// GetPackageManifest will retrieve the manifest file for the specified package
func GetPackageManifest(p PackageService, loc loc.Locator) (*Manifest, error) {
	_, reader, err := p.ReadPackage(loc)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	decompressed, err := dockerarchive.DecompressStream(reader)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer decompressed.Close()
	tarball := tar.NewReader(decompressed)

	manifest, err := ReadManifest(tarball)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return manifest, nil
}

// ConfigurePackage reads the given package, and configures it using arguments passed,
// the resulting package is created within the scope of the same package service
func ConfigurePackage(p PackageService, loc loc.Locator, confLoc loc.Locator, args []string, labels map[string]string) error {
	reader, err := GetConfigPackage(p, loc, confLoc, args)
	if err != nil {
		return trace.Wrap(err)
	}
	allLabels := map[string]string{
		ConfigLabel: loc.ZeroVersion().String(),
	}
	for k, v := range labels {
		allLabels[k] = v
	}
	_, err = p.CreatePackage(confLoc, reader, WithLabels(allLabels))
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// ExecutePackageCommand executes command specified in the package and returns
// results of CombinedOutput call on the package binary
func ExecutePackageCommand(p PackageService, cmd string, loc loc.Locator, confLoc *loc.Locator, execArgs []string, storageDir string) ([]byte, error) {
	log.Infof("exec with config %v %v", loc, confLoc)

	unpackedPath := PackagePath(storageDir, loc)
	if err := Unpack(p, loc, unpackedPath, nil); err != nil {
		return nil, trace.Wrap(err)
	}
	manifest, err := OpenManifest(unpackedPath)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if err := Unpack(p, loc, unpackedPath, nil); err != nil {
		return nil, trace.Wrap(err)
	}

	manifestCmdSpec, err := manifest.Command(cmd)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	env := []string{fmt.Sprintf("PATH=%v", os.Getenv("PATH"))}
	// read package with configuration if it's provided
	if confLoc != nil && confLoc.Name != "" {
		_, reader, err := p.ReadPackage(*confLoc)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		defer reader.Close()

		vars, err := ReadConfigPackage(reader)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		for k, v := range vars {
			env = append(env, fmt.Sprintf("%v=%v", k, v))
		}
	}

	args := append(manifestCmdSpec.Args, execArgs...)
	command := exec.Command(args[0], args[1:]...)
	command.Dir = unpackedPath
	command.Env = env

	log.Infof("ExecutePackageCommand(%v %v %v, unpacked=%v)",
		manifestCmdSpec.Args[0], cmd, execArgs, unpackedPath)

	out, err := command.CombinedOutput()
	if err != nil {
		return out, trace.Wrap(err)
	}
	return out, nil
}

// FindPackage finds package matching the predicate fn
func FindPackage(packages PackageService, fn func(e PackageEnvelope) bool) (*PackageEnvelope, error) {
	var env *PackageEnvelope
	err := ForeachPackage(packages, func(e PackageEnvelope) error {
		if fn(e) {
			env = &e
		}
		return nil
	})
	if env == nil {
		return nil, trace.NotFound("package not found")
	}
	return env, trace.Wrap(err)
}

// ForeachPackage executes function fn for each package in
// each repository
func ForeachPackage(packages PackageService, fn func(e PackageEnvelope) error) error {
	repos, err := packages.GetRepositories()
	if err != nil {
		return trace.Wrap(err)
	}
	for _, repo := range repos {
		if err := ForeachPackageInRepo(packages, repo, fn); err != nil {
			return trace.Wrap(err)
		}
	}
	return nil
}

// ForeachPackageInRepo executes fn for each package found in repository
func ForeachPackageInRepo(packages PackageService, repo string, fn func(e PackageEnvelope) error) error {
	packs, err := packages.GetPackages(repo)
	if err != nil {
		return trace.Wrap(err)
	}
	for _, pkg := range packs {
		if err := fn(pkg); err != nil {
			return trace.Wrap(err)
		}
	}
	return nil
}

// FindInstalledPackage finds package currently installed on the host
func FindInstalledPackage(packages PackageService, filter loc.Locator) (*loc.Locator, error) {
	pkg, err := FindPackage(packages, func(e PackageEnvelope) bool {
		if e.Locator.Repository != filter.Repository {
			return false
		}
		if e.Locator.Name != filter.Name {
			return false
		}
		return e.HasLabel(InstalledLabel, InstalledLabel)
	})
	if err != nil {
		if trace.IsNotFound(err) {
			return nil, trace.NotFound("no installed package for %v not found",
				filter)
		}
		return nil, trace.Wrap(err)
	}
	return &pkg.Locator, nil
}

// FindConfigPackage returns configuration package for given package
func FindConfigPackage(packages PackageService, filter loc.Locator) (*loc.Locator, error) {
	configPkg, err := FindPackage(packages, func(e PackageEnvelope) bool {
		return e.HasLabel(ConfigLabel, filter.ZeroVersion().String())
	})
	if err != nil {
		if trace.IsNotFound(err) {
			return nil, trace.NotFound("no configuration package for %v found",
				filter)
		}
		return nil, trace.Wrap(err)
	}
	return &configPkg.Locator, nil
}

// FindInstalledPackageWithConfig finds installed package and associated configuration package
func FindInstalledPackageWithConfig(packages PackageService, filter loc.Locator) (*loc.Locator, *loc.Locator, error) {
	locator, err := FindInstalledPackage(packages, filter)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}

	configLocator, err := FindConfigPackage(packages, *locator)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}

	return locator, configLocator, nil
}

// ProcessMetadata processes some special metadata conventions, e.g. 'latest' metadata label
func ProcessMetadata(packages PackageService, loc *loc.Locator) (*loc.Locator, error) {
	ver, err := loc.SemVer()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if ver.Metadata == LatestLabel {
		return FindLatestPackage(packages, *loc)
	}
	return loc, nil
}

// FindLatestPackageWithLabels returns the latest package matching the provided
// labels
func FindLatestPackageWithLabels(packages PackageService, repository string, labels map[string]string) (*loc.Locator, error) {
	loc, err := FindLatestPackagePredicate(packages, repository, func(e PackageEnvelope) bool {
		return e.HasLabels(labels)
	})
	if err != nil && trace.IsNotFound(err) {
		return nil, trace.NotFound("latest package in repo %q with labels %v not found", repository, labels)
	}
	return loc, trace.Wrap(err)
}

// FindLatestPackage returns package the latest package matching the provided
// locator
func FindLatestPackage(packages PackageService, filter loc.Locator) (*loc.Locator, error) {
	loc, err := FindLatestPackagePredicate(packages, filter.Repository, func(e PackageEnvelope) bool {
		return e.Locator.Repository == filter.Repository &&
			e.Locator.Name == filter.Name
	})
	if err != nil && trace.IsNotFound(err) {
		return nil, trace.NotFound("latest package with filter %v not found", filter)
	}
	return loc, trace.Wrap(err)
}

// FindLatestPackageByName returns latest package with the specified name (across all repos)
func FindLatestPackageByName(packages PackageService, name string) (*loc.Locator, error) {
	loc, err := FindLatestPackagePredicate(packages, "", func(e PackageEnvelope) bool {
		return e.Locator.Name == name
	})
	if err != nil && trace.IsNotFound(err) {
		return nil, trace.NotFound("latest package with name %q not found", name)
	}
	return loc, trace.Wrap(err)
}

// FindLatestPackagePredicate returns the latest package matching the provided
// predicate function
//
// If the provided repository is empty, searches all repositories.
func FindLatestPackagePredicate(packages PackageService, repository string, filter func(PackageEnvelope) bool) (*loc.Locator, error) {
	var max *loc.Locator
	predicate := func(e PackageEnvelope) error {
		if !filter(e) {
			return nil
		}
		if max == nil {
			max = &e.Locator
			return nil
		}
		vera, err := max.SemVer()
		if err != nil {
			return nil
		}
		verb, err := e.Locator.SemVer()
		if err != nil {
			return nil
		}
		if verb.Compare(*vera) > 0 {
			max = &e.Locator
		}
		return nil
	}
	var err error
	if repository != "" {
		err = ForeachPackageInRepo(packages, repository, predicate)
	} else {
		err = ForeachPackage(packages, predicate)
	}
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if max == nil {
		return nil, trace.NotFound("latest package not found")
	}
	return max, nil
}

// FindNewerPackages returns packages with versions greater than in the provided package
func FindNewerPackages(packages PackageService, filter loc.Locator) ([]loc.Locator, error) {
	var result []loc.Locator
	err := ForeachPackageInRepo(packages, filter.Repository, func(e PackageEnvelope) error {
		if e.Locator.Name != filter.Name {
			return nil
		}
		vera, err := filter.SemVer()
		if err != nil {
			return trace.Wrap(err)
		}
		verb, err := e.Locator.SemVer()
		if err != nil {
			return trace.Wrap(err)
		}
		if verb.Compare(*vera) > 0 {
			result = append(result, e.Locator)
		}
		return nil
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return result, nil
}

// FindPackageUpdate determines if an update is available for package specified with pkg
// and returns a descriptor object to go from existing version to a new one.
// If no update can be found, returns a nil descriptor and an instance of trace.NotFound as error
func FindPackageUpdate(packages PackageService, pkg loc.Locator) (*storage.PackageUpdate, error) {
	latestPackage, err := FindLatestPackage(packages, pkg)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	currentVersion, err := pkg.SemVer()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	latestVersion, err := latestPackage.SemVer()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if latestVersion.Compare(*currentVersion) > 0 {
		return &storage.PackageUpdate{From: pkg, To: *latestPackage}, nil
	}
	return nil, trace.NotFound("%v is already at the latest version", pkg)
}

// CheckUpdatePackage makes sure that "to" package is acceptable when updating from "from" package
func CheckUpdatePackage(from, to loc.Locator) error {
	// repository and package name must match
	if from.Repository != to.Repository || from.Name != to.Name {
		return trace.BadParameter(
			"you are attempting to upgrade to %v %v, but different application is installed: %v %v",
			to.Name, to.Version, from.Name, from.Version)
	}
	// do not allow downgrades
	fromVer, err := from.SemVer()
	if err != nil {
		return trace.Wrap(err)
	}
	toVer, err := to.SemVer()
	if err != nil {
		return trace.Wrap(err)
	}
	if !fromVer.LessThan(*toVer) {
		return trace.BadParameter(
			"update version (%v) must be greater than the currently installed version (%v)", toVer, fromVer)
	}
	return nil
}

// ConfigLabels returns the label set to assign a configuration role for the specified package loc
func ConfigLabels(loc loc.Locator, purpose string) map[string]string {
	return map[string]string{
		ConfigLabel:  loc.ZeroVersion().String(),
		PurposeLabel: purpose,
	}
}
