package rsync

import (
	"bytes"
	"text/template"
)

type RsyncConfigModule struct {
	ModuleName  string
	Path        string
	Comment     string
	HostAllow   string
	IgnorePerms bool
	UID         string
	GID         string
	AuthUsers   *[]string
	Secrets     *string
	Readonly    bool
}

type RsyncConfigModuleSpec struct {
	ModuleName string
	Path       string
	HostAllow  string
}

type RsyncConf []RsyncConfigModule

const rsyncConfTemplate = `
{{ range . }}
[{{ .ModuleName }}]
	path = {{ .Path }}
	read only = {{ .Readonly }}
	comment = {{ .Comment }}
	hosts allow = {{ .HostAllow }}
	list = yes
	uid = {{ .UID }}
	gid = {{ .GID }}
	ignore perms = {{ .IgnorePerms }}
{{ end }}
`

// TODO: Add auth
func NewRsyncModuleConfig(spec RsyncConfigModuleSpec) RsyncConfigModule {
	return RsyncConfigModule{
		ModuleName: spec.ModuleName,
		Path:       spec.Path,
		// TODO: Dont do this
		HostAllow:   "*",
		Comment:     spec.ModuleName,
		IgnorePerms: true,
		Readonly:    false,
		UID:         "root",
		GID:         "root",
	}
}

func NewRsyncConf(moduleConfigs ...RsyncConfigModule) RsyncConf {
	res := make(RsyncConf, 0)
	res = append(res, moduleConfigs...)
	return res
}

func (r *RsyncConf) GenerateRsyncConfFile() (string, error) {
	template, err := template.New("rsyncd.conf").Parse(rsyncConfTemplate)
	if err != nil {
		return "", err
	}
	buf := new(bytes.Buffer)
	err = template.Execute(buf, r)
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}
