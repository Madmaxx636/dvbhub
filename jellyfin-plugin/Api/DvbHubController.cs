using System;
using System.IO;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;
using System.Threading.Tasks;
using Microsoft.AspNetCore.Authorization;
using Microsoft.AspNetCore.Http;
using Microsoft.AspNetCore.Mvc;

namespace Jellyfin.Plugin.DvbHub.Api;

/// <summary>
/// Admin-only proxy from the Jellyfin dashboard to dvbhub's JSON API, so the
/// browser only needs to reach Jellyfin and Jellyfin's login protects dvbhub.
/// </summary>
[ApiController]
[Authorize(Policy = "RequiresElevation")]
[Route("DvbHub")]
public class DvbHubController : ControllerBase
{
    private readonly IHttpClientFactory _httpClientFactory;

    /// <summary>
    /// Initializes a new instance of the <see cref="DvbHubController"/> class.
    /// </summary>
    /// <param name="httpClientFactory">HTTP client factory.</param>
    public DvbHubController(IHttpClientFactory httpClientFactory)
    {
        _httpClientFactory = httpClientFactory;
    }

    /// <summary>
    /// Forwards a request to dvbhub's /api/{path}.
    /// </summary>
    /// <param name="path">API path below /api/.</param>
    /// <returns>dvbhub's response.</returns>
    [AcceptVerbs("GET", "POST", "PUT", "DELETE")]
    [Route("Api/{**path}")]
    public async Task<IActionResult> Proxy([FromRoute] string path)
    {
        var config = Plugin.Instance?.Configuration;
        if (config is null)
        {
            return Fail(StatusCodes.Status503ServiceUnavailable, "plugin not initialised");
        }

        if (string.IsNullOrWhiteSpace(path) || path.Contains("..", StringComparison.Ordinal) || path.Contains("://", StringComparison.Ordinal))
        {
            return Fail(StatusCodes.Status400BadRequest, "invalid path");
        }

        var baseUrl = config.DvbHubUrl.TrimEnd('/');
        var target = baseUrl + "/api/" + path + Request.QueryString.Value;
        using var message = new HttpRequestMessage(new HttpMethod(Request.Method), target);

        if (HttpMethods.IsPost(Request.Method) || HttpMethods.IsPut(Request.Method))
        {
            var body = new MemoryStream();
            await Request.Body.CopyToAsync(body, HttpContext.RequestAborted).ConfigureAwait(false);
            body.Position = 0;
            message.Content = new StreamContent(body);
            message.Content.Headers.ContentType = MediaTypeHeaderValue.Parse(Request.ContentType ?? "application/json");
        }

        if (!string.IsNullOrEmpty(config.AdminPassword))
        {
            var token = Convert.ToBase64String(Encoding.UTF8.GetBytes("admin:" + config.AdminPassword));
            message.Headers.Authorization = new AuthenticationHeaderValue("Basic", token);
        }

        try
        {
            var client = _httpClientFactory.CreateClient();
            client.Timeout = TimeSpan.FromSeconds(30);
            using var response = await client.SendAsync(message, HttpContext.RequestAborted).ConfigureAwait(false);
            var content = await response.Content.ReadAsStringAsync(HttpContext.RequestAborted).ConfigureAwait(false);
            if (response.StatusCode == System.Net.HttpStatusCode.Unauthorized)
            {
                return Fail(StatusCodes.Status502BadGateway, "dvbhub rejected the password; set it on the plugin's Setup tab");
            }

            return new ContentResult
            {
                StatusCode = (int)response.StatusCode,
                Content = content,
                ContentType = response.Content.Headers.ContentType?.ToString() ?? "application/json"
            };
        }
        catch (Exception ex) when (ex is HttpRequestException or TaskCanceledException)
        {
            return Fail(StatusCodes.Status502BadGateway, $"cannot reach dvbhub at {baseUrl}: {ex.Message}");
        }
    }

    private static ContentResult Fail(int status, string message) => new()
    {
        StatusCode = status,
        Content = JsonSerializer.Serialize(new { error = message }),
        ContentType = "application/json"
    };
}
