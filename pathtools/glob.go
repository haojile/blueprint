// Copyright 2014 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pathtools

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/blueprint/deptools"
)

var GlobMultipleRecursiveErr = errors.New("pattern contains multiple **")
var GlobLastRecursiveErr = errors.New("pattern ** as last path element")

// Glob returns the list of files that match the given pattern along with the
// list of directories that were searched to construct the file list.
// The supported glob patterns are equivalent to filepath.Glob, with an
// extension that recursive glob (** matching zero or more complete path
// entries) is supported. Glob also returns a list of directories that were
// searched.
//
// In general ModuleContext.GlobWithDeps or SingletonContext.GlobWithDeps
// should be used instead, as they will automatically set up dependencies
// to rerun the primary builder when the list of matching files changes.
func Glob(pattern string) (matches, dirs []string, err error) {
	return GlobWithExcludes(pattern, nil)
}

// GlobWithExcludes returns the list of files that match the given pattern but
// do not match the given exclude patterns, along with the list of directories
// that were searched to construct the file list.  The supported glob and
// exclude patterns are equivalent to filepath.Glob, with an extension that
// recursive glob (** matching zero or more complete path entries) is supported.
// GlobWithExcludes also returns a list of directories that were searched.
//
// In general ModuleContext.GlobWithDeps or SingletonContext.GlobWithDeps
// should be used instead, as they will automatically set up dependencies
// to rerun the primary builder when the list of matching files changes.
func GlobWithExcludes(pattern string, excludes []string) (matches, dirs []string, err error) {
	if filepath.Base(pattern) == "**" {
		return nil, nil, GlobLastRecursiveErr
	} else {
		matches, dirs, err = glob(pattern, false)
	}

	if err != nil {
		return nil, nil, err
	}

	matches, err = filterExcludes(matches, excludes)
	if err != nil {
		return nil, nil, err
	}

	return matches, dirs, nil
}

// glob is a recursive helper function to handle globbing each level of the pattern individually,
// allowing searched directories to be tracked.  Also handles the recursive glob pattern, **.
func glob(pattern string, hasRecursive bool) (matches, dirs []string, err error) {
	if !isWild(pattern) {
		// If there are no wilds in the pattern, check whether the file exists or not.
		// Uses filepath.Glob instead of manually statting to get consistent results.
		pattern = filepath.Clean(pattern)
		matches, err = filepath.Glob(pattern)
		if err != nil {
			return matches, dirs, err
		}

		if len(matches) == 0 {
			// Some part of the non-wild pattern didn't exist.  Add the last existing directory
			// as a dependency.
			var matchDirs []string
			for len(matchDirs) == 0 {
				pattern, _ = saneSplit(pattern)
				matchDirs, err = filepath.Glob(pattern)
				if err != nil {
					return matches, dirs, err
				}
			}
			dirs = append(dirs, matchDirs...)
		}
		return matches, dirs, err
	}

	dir, file := saneSplit(pattern)

	if file == "**" {
		if hasRecursive {
			return matches, dirs, GlobMultipleRecursiveErr
		}
		hasRecursive = true
	}

	dirMatches, dirs, err := glob(dir, hasRecursive)
	if err != nil {
		return nil, nil, err
	}

	for _, m := range dirMatches {
		info, err := os.Stat(m)
		if err != nil {
			return nil, nil, fmt.Errorf("unexpected error after glob: %s", err)
		}
		if info.IsDir() {
			if file == "**" {
				recurseDirs, err := walkAllDirs(m)
				if err != nil {
					return nil, nil, err
				}
				matches = append(matches, recurseDirs...)
			} else {
				dirs = append(dirs, m)
				newMatches, err := filepath.Glob(filepath.Join(m, file))
				if err != nil {
					return nil, nil, err
				}
				matches = append(matches, newMatches...)
			}
		}
	}

	return matches, dirs, nil
}

// Faster version of dir, file := filepath.Dir(path), filepath.File(path) with no allocations
// Similar to filepath.Split, but returns "." if dir is empty and trims trailing slash if dir is
// not "/".  Returns ".", "" if path is "."
func saneSplit(path string) (dir, file string) {
	if path == "." {
		return ".", ""
	}
	dir, file = filepath.Split(path)
	switch dir {
	case "":
		dir = "."
	case "/":
		// Nothing
	default:
		dir = dir[:len(dir)-1]
	}
	return dir, file
}

func isWild(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

// Returns a list of all directories under dir
func walkAllDirs(dir string) (dirs []string, err error) {
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.Mode().IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})

	return dirs, err
}

// Filters the strings in matches based on the glob patterns in excludes.  Hierarchical (a/*) and
// recursive (**) glob patterns are supported.
func filterExcludes(matches []string, excludes []string) ([]string, error) {
	if len(excludes) == 0 {
		return matches, nil
	}

	var ret []string
matchLoop:
	for _, m := range matches {
		for _, e := range excludes {
			exclude, err := match(e, m)
			if err != nil {
				return nil, err
			}
			if exclude {
				continue matchLoop
			}
		}
		ret = append(ret, m)
	}

	return ret, nil
}

// match returns true if name matches pattern using the same rules as filepath.Match, but supporting
// hierarchical patterns (a/*) and recursive globs (**).
func match(pattern, name string) (bool, error) {
	if filepath.Base(pattern) == "**" {
		return false, GlobLastRecursiveErr
	}

	for {
		var patternFile, nameFile string
		pattern, patternFile = saneSplit(pattern)
		name, nameFile = saneSplit(name)

		if patternFile == "**" {
			return matchPrefix(pattern, filepath.Join(name, nameFile))
		}

		if nameFile == "" && patternFile == "" {
			return true, nil
		} else if nameFile == "" || patternFile == "" {
			return false, nil
		}

		match, err := filepath.Match(patternFile, nameFile)
		if err != nil || !match {
			return match, err
		}
	}
}

// matchPrefix returns true if the beginning of name matches pattern using the same rules as
// filepath.Match, but supporting hierarchical patterns (a/*).  Recursive globs (**) are not
// supported, they should have been handled in match().
func matchPrefix(pattern, name string) (bool, error) {
	if len(pattern) > 0 && pattern[0] == '/' {
		if len(name) > 0 && name[0] == '/' {
			pattern = pattern[1:]
			name = name[1:]
		} else {
			return false, nil
		}
	}

	for {
		var patternElem, nameElem string
		patternElem, pattern = saneSplitFirst(pattern)
		nameElem, name = saneSplitFirst(name)

		if patternElem == "." {
			patternElem = ""
		}
		if nameElem == "." {
			nameElem = ""
		}

		if patternElem == "**" {
			return false, GlobMultipleRecursiveErr
		}

		if patternElem == "" {
			return true, nil
		} else if nameElem == "" {
			return false, nil
		}

		match, err := filepath.Match(patternElem, nameElem)
		if err != nil || !match {
			return match, err
		}
	}
}

func saneSplitFirst(path string) (string, string) {
	i := strings.IndexRune(path, filepath.Separator)
	if i < 0 {
		return path, ""
	}
	return path[:i], path[i+1:]
}

func GlobPatternList(patterns []string, prefix string) (globedList []string, depDirs []string, err error) {
	var (
		matches []string
		deps    []string
	)

	globedList = make([]string, 0)
	depDirs = make([]string, 0)

	for _, pattern := range patterns {
		if isWild(pattern) {
			matches, deps, err = Glob(filepath.Join(prefix, pattern))
			if err != nil {
				return nil, nil, err
			}
			globedList = append(globedList, matches...)
			depDirs = append(depDirs, deps...)
		} else {
			globedList = append(globedList, filepath.Join(prefix, pattern))
		}
	}
	return globedList, depDirs, nil
}

// IsGlob returns true if the pattern contains any glob characters (*, ?, or [).
func IsGlob(pattern string) bool {
	return strings.IndexAny(pattern, "*?[") >= 0
}

// HasGlob returns true if any string in the list contains any glob characters (*, ?, or [).
func HasGlob(in []string) bool {
	for _, s := range in {
		if IsGlob(s) {
			return true
		}
	}

	return false
}

// GlobWithDepFile finds all files that match glob.  It compares the list of files
// against the contents of fileListFile, and rewrites fileListFile if it has changed.  It also
// writes all of the the directories it traversed as a depenencies on fileListFile to depFile.
//
// The format of glob is either path/*.ext for a single directory glob, or path/**/*.ext
// for a recursive glob.
//
// Returns a list of file paths, and an error.
//
// In general ModuleContext.GlobWithDeps or SingletonContext.GlobWithDeps
// should be used instead, as they will automatically set up dependencies
// to rerun the primary builder when the list of matching files changes.
func GlobWithDepFile(glob, fileListFile, depFile string, excludes []string) (files []string, err error) {
	files, dirs, err := GlobWithExcludes(glob, excludes)
	if err != nil {
		return nil, err
	}

	fileList := strings.Join(files, "\n") + "\n"

	WriteFileIfChanged(fileListFile, []byte(fileList), 0666)
	deptools.WriteDepFile(depFile, fileListFile, dirs)

	return
}

// WriteFileIfChanged wraps ioutil.WriteFile, but only writes the file if
// the files does not already exist with identical contents.  This can be used
// along with ninja restat rules to skip rebuilding downstream rules if no
// changes were made by a rule.
func WriteFileIfChanged(filename string, data []byte, perm os.FileMode) error {
	var isChanged bool

	dir := filepath.Dir(filename)
	err := os.MkdirAll(dir, 0777)
	if err != nil {
		return err
	}

	info, err := os.Stat(filename)
	if err != nil {
		if os.IsNotExist(err) {
			// The file does not exist yet.
			isChanged = true
		} else {
			return err
		}
	} else {
		if info.Size() != int64(len(data)) {
			isChanged = true
		} else {
			oldData, err := ioutil.ReadFile(filename)
			if err != nil {
				return err
			}

			if len(oldData) != len(data) {
				isChanged = true
			} else {
				for i := range data {
					if oldData[i] != data[i] {
						isChanged = true
						break
					}
				}
			}
		}
	}

	if isChanged {
		err = ioutil.WriteFile(filename, data, perm)
		if err != nil {
			return err
		}
	}

	return nil
}
