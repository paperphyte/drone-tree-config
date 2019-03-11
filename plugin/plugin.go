package plugin

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/drone/drone-go/drone"
	"github.com/drone/drone-go/plugin/config"

	"github.com/google/go-github/github"
	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
)

func New(server, token string, concat bool) config.Plugin {
	return &plugin{
		server: server,
		token:  token,
		concat: concat,
	}
}

type plugin struct {
	server string
	token  string
	concat bool
}

func (p *plugin) Find(ctx context.Context, req *config.Request) (*drone.Config, error) {
	// log
	logrus.Infof("Handling %s %s: %s to %s", req.Repo.Namespace, req.Repo.Name, req.Build.Before, req.Build.After)
	logrus.Debugf("Build: %+v", req.Build)
	logrus.Debugf("Repo: %+v", req.Repo)

	// github client
	client := &github.Client{}

	// creates a github transport that authenticates
	// http requests using the github access token.
	trans := oauth2.NewClient(ctx, oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: p.token},
	))

	// connect with github
	if p.server == "" {
		client = github.NewClient(trans)
	} else {
		var err error
		client, err = github.NewEnterpriseClient(p.server, p.server, trans)
		if err != nil {
			logrus.Errorf("Unable to connect to Github: '%v', server: '%s'", err, p.server)
			return nil, err
		}
	}

	// get repo changes
	changedFiles := []string{}
	if req.Build.Fork != "" {
		// use fork api to get changed files
		pullRequestId, err := strconv.Atoi(strings.Split(req.Build.Ref, "/")[2])
		if err != nil {
			logrus.Errorf("Unable to get pull request id: %v", err)
			return nil, err
		}
		opts := github.ListOptions{}
		files, _, err := client.PullRequests.ListFiles(ctx, req.Repo.Namespace, req.Repo.Name, pullRequestId, &opts)
		if err != nil {
			logrus.Errorf("Unable to fetch diff for Pull request: %v", err)
		}
		for _, file := range files {
			changedFiles = append(changedFiles, *file.Filename)
		}
	} else {
		// use diff to get changed files
		changes, _, err := client.Repositories.CompareCommits(ctx, req.Repo.Namespace, req.Repo.Name, req.Build.Before, req.Build.After)
		if err != nil {
			logrus.Errorf("Unable to fetch diff: '%v', server: '%s'", err, p.server)
			return nil, err
		}
		for _, file := range changes.Files {
			changedFiles = append(changedFiles, *file.Filename)
		}
	}

	// collect drone.yml files
	files := map[string]string{}
	order := []string{}
	cache := map[string]bool{}
	for _, file := range changedFiles {
		if !strings.HasPrefix(file, "/") {
			file = "/" + file
		}

		done := false
		dir := path.Join(file, "..")
		for !done {
			done = bool(dir == "/")
			dir = path.Join(dir, "..")
			file := path.Join(dir, req.Repo.Config)

			// check if file has already been checked
			_, ok := cache[file]
			if ok {
				continue
			} else {
				cache[file] = true
			}

			// check file on github
			content, err := p.getGithubFile(ctx, req, client, file)
			if err != nil {
				logrus.Debugf("Unable to load file: %s %v", file, err)
			} else {
				logrus.Infof("Found %s/%s %s", req.Repo.Namespace, req.Repo.Name, file)
				order = append(order, file)
				files[file] = content
			}
		}
	}

	// no file found
	if len(files) == 0 {
		return nil, errors.New("Did not found a .drone.yml")
	}

	// return first if concat is false otherwise return all as multi-machine
	content := ""
	if !p.concat {
		fileName := order[0]
		logrus.Infof("Only shipping first match: %s", fileName)
		content = files[fileName]
	} else {
		for _, fileName := range order {
			fileContent := files[fileName]
			fileName = fmt.Sprintf("# .drone.yml origin: %s\n", fileName)
			if content != "" {
				content += "\n---\n"
			}
			content += fileName + fileContent + "\n"
		}

	}

	return &drone.Config{Data: content}, nil
}

// get the contents of a file on github, if the file is not found throw an error
func (p *plugin) getGithubFile(ctx context.Context, req *config.Request, client *github.Client, file string) (string, error) {
	logrus.Infof("Testing %s/%s %s", req.Repo.Namespace, req.Repo.Name, file)

	ref := github.RepositoryContentGetOptions{Ref: req.Build.After}
	data, _, _, err := client.Repositories.GetContents(ctx, req.Repo.Namespace, req.Repo.Name, file, &ref)
	if err != nil {
		return "", err
	}

	content, err := data.GetContent()
	if err != nil {
		return "", err
	}

	return content, nil
}
