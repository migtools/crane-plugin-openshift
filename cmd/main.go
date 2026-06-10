package main

import (
	openshift "crane-plugin-openshift"

	"github.com/konveyor/crane-lib/transform/cli"
	"github.com/sirupsen/logrus"
)

func main() {
	plugin := &openshift.OpenShiftTransformPlugin{
		Log: logrus.New(),
	}
	meta := plugin.Metadata()
	cli.RunAndExit(cli.NewCustomPlugin(meta.Name, openshift.PluginVersion, meta.OptionalFields, plugin.Run))
}
