// Config is put into a different package to prevent cyclic imports in case
// it is needed in several locations

package config

type Config struct {
	Lambdabeat LambdabeatConfig
}

type LambdabeatConfig struct {
	Period    string   `config:"period"`   // how often to run lambdabeat
	Interval  int64    `config:"interval"` // time interval for statistics
	Functions []string `config:"functions"`
	Region    string   `config:"region"`
}
