using MediaBrowser.Model.Plugins;

namespace Jellyfin.Plugin.DvbHub.Configuration;

/// <summary>
/// Plugin configuration: where dvbhub is and how to authenticate to it.
/// </summary>
public class PluginConfiguration : BasePluginConfiguration
{
    /// <summary>
    /// Gets or sets the dvbhub base URL as reachable from the Jellyfin server.
    /// </summary>
    public string DvbHubUrl { get; set; } = "http://localhost:9980";

    /// <summary>
    /// Gets or sets dvbhub's admin password (empty if dvbhub has none).
    /// </summary>
    public string AdminPassword { get; set; } = string.Empty;
}
