package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

type Restic struct {
	errb *bytes.Buffer
	outb *bytes.Buffer
}

func NewRestic(errb *bytes.Buffer, outb *bytes.Buffer) *Restic {
	r := &Restic{}
	r.errb = errb
	r.outb = outb
	return r
}

type B2Options struct {
	B2AccountId  string `json:"b2_account_id"`
	B2AccountKey string `json:"b2_account_key"`
}

type AzureOptions struct {
	AzureAccountName    string `json:"azure_account_name"`
	AzureAccountKey     string `json:"azure_account_key"`
	AzureAccountSas     string `json:"azure_account_sas"`
	AzureEndpointSuffix string `json:"azure_endpoint_suffix"`
}

type Options struct {
	B2Options
	AzureOptions
}

type RepositoryType int32

const (
	LOCAL  RepositoryType = iota
	B2     RepositoryType = iota
	AWS    RepositoryType = iota
	AZURE  RepositoryType = iota
	GOOGLE RepositoryType = iota
)

type Snapshot struct {
	Id             string   `json:"id"`
	Time           string   `json:"time"`
	Paths          []string `json:"paths"`
	Hostname       string   `json:"hostname"`
	Username       string   `json:"username"`
	UID            uint32   `json:"uid"`
	GID            uint32   `json:"gid"`
	ShortId        string   `json:"short_id"`
	Tags           []string `json:"tags"`
	ProgramVersion string   `json:"program_version"`
}

func (r *Restic) core(repository Repository, cmd []string, envs []string) (string, error) {

	cmds := []string{"-r", repository.Path, "--json"}
	cmds = append(cmds, cmd...)
	var sout bytes.Buffer
	var serr bytes.Buffer
	c := exec.Command("/usr/bin/restic", cmds...)
	c.Stderr = &serr
	c.Stdout = &sout
	c.Env = append(os.Environ(), "RESTIC_PASSWORD="+repository.Password)

	err := c.Start()
	if err != nil {
		fmt.Println(err)
	}
	c.Wait()
	r.errb.Write(serr.Bytes())
	r.outb.Write(sout.Bytes())

	return sout.String(), nil

}

func (r *Restic) Unlock(repository Repository) {
	if _, err := r.core(repository, []string{"unlock"}, []string{}); err != nil {
		fmt.Println("ERROR", err)
	}
}

func (r *Restic) Check(repository Repository) error {
	if _, err := r.core(repository, []string{"check"}, []string{}); err != nil {
		return err
	}
	return nil
}

func (r *Restic) Initialize(repository Repository) error {
	if _, err := r.core(repository, []string{"init"}, []string{}); err != nil {
		return err
	}
	return nil
}

func (r *Restic) Snapshots(repository Repository) []Snapshot {
	if res, err := r.core(repository, []string{"snapshots"}, []string{}); err == nil {
		var data []Snapshot
		if err := json.Unmarshal([]byte(res), &data); err == nil {
			return data
		}
	} else {
		fmt.Println("ERROR", err)
	}

	return []Snapshot{}
}

func (r *Restic) RunBackup(backup *Backup, toRepository *Repository, fromRepository *Repository) {
	time.Sleep(30 * time.Second)

	if backup == nil && toRepository == nil || fromRepository == nil && toRepository == nil {
		fmt.Println("Nope!")
		return
	}

	if backup != nil && fromRepository != nil {
		fmt.Println("Nope!")
		return
	}

	if backup != nil {
		cmds := []string{"backup"}
		for _, p := range backup.BackupParams {
			cmds = append(cmds, p...)
		}
		fmt.Println(cmds)
		// r.core(*toRepository, cmds, []string{})
	}

	if fromRepository != nil {
		cmds := []string{"copy", "--from-repo", fromRepository.Path}
		envs := []string{"RESTIC_FROM_PASSWORD", fromRepository.Password}
		fmt.Println(cmds)
		fmt.Println(envs)
		// r.core(*toRepository, cmds, []string{})
	}

}
