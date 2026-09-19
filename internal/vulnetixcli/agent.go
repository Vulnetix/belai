package vulnetixcli

// InstallAgentAssets installs the Vulnetix CLI's AI-agent hooks and skills
// directly into the harness. The returned output is trusted internal content.
func (c *CLI) InstallAgentAssets() (string, error) {
	return c.Run("agent", "install")
}
