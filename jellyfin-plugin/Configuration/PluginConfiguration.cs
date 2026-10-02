using MediaBrowser.Model.Plugins;

namespace Jellyfin.Plugin.DvbHub.Configuration;

/// <summary>
/// Plugin configuration: where dvbhub is and how to log in to it.
/// </summary>
public class PluginConfiguration : BasePluginConfiguration
{
    /// <summary>
    /// Gets or sets dvbhub's address as reachable from the Jellyfin server.
    /// </summary>
    public string DvbHubUrl { get; set; } = string.Empty;

    /// <summary>
    /// Gets or sets dvbhub's password (empty if dvbhub has none).
    /// </summary>
    public string AdminPassword { get; set; } = string.Empty;
}
