// Config is put into a different package to prevent cyclic imports in case
// it is needed in several locations

package config

type Config struct {
	Lambdabeat LambdabeatConfig
}

type LambdabeatConfig struct {
	Period    string   `config:"period"`   // how often to run lambdabeat
	Interval  string   `config:"interval"` // time interval for statistics
	Functions []string `config:"functions"`
	Metrics   []string `config:"metrics"`
}
