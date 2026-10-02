using System;
using System.Collections.Generic;
using Jellyfin.Plugin.DvbHub.Configuration;
using MediaBrowser.Common.Configuration;
using MediaBrowser.Common.Plugins;
using MediaBrowser.Model.Plugins;
using MediaBrowser.Model.Serialization;

namespace Jellyfin.Plugin.DvbHub;

/// <summary>
/// dvbhub plugin: control a dvbhub TV tuner server from Jellyfin's dashboard.
/// </summary>
public class Plugin : BasePlugin<PluginConfiguration>, IHasWebPages
{
    /// <summary>
    /// Initializes a new instance of the <see cref="Plugin"/> class.
    /// </summary>
    /// <param name="applicationPaths">Application paths.</param>
    /// <param name="xmlSerializer">XML serializer.</param>
    public Plugin(IApplicationPaths applicationPaths, IXmlSerializer xmlSerializer)
        : base(applicationPaths, xmlSerializer)
    {
        Instance = this;
    }

    /// <summary>Gets the running plugin instance.</summary>
    public static Plugin? Instance { get; private set; }

    /// <inheritdoc />
    public override string Name => "dvbhub";

    /// <inheritdoc />
    public override Guid Id => Guid.Parse("7b1e4c2a-5d3f-4e8b-9a61-2c0f8d9e4b17");

    /// <inheritdoc />
    public override string Description =>
        "Control your dvbhub TV tuner server from Jellyfin: channel scanning, channels, guide, signal bars, transcoding, tuners, drivers and antenna alignment.";

    /// <inheritdoc />
    public IEnumerable<PluginPageInfo> GetPages()
    {
        return new[]
        {
            new PluginPageInfo
            {
                Name = "dvbhub",
                DisplayName = "TV Tuners (dvbhub)",
                EmbeddedResourcePath = GetType().Namespace + ".Web.dvbhub.html",
                EnableInMainMenu = true,
                MenuSection = "server",
                MenuIcon = "settings_input_antenna"
            }
        };
    }
}
