package br.com.p2pplayer.net

import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.TimeUnit

data class IceServerConfig(
    val urls: List<String>,
    val username: String? = null,
    val credential: String? = null,
)

data class SessionResponse(
    val sessionId: String,
    val streamKey: String,
    val manifestUrl: String,
    val wsUrl: String,
    val iceServers: List<IceServerConfig>,
)

class ApiClient(
    private val baseUrl: String,
    private val accessSecret: String? = null,
) {
    private val client = OkHttpClient.Builder()
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(30, TimeUnit.SECONDS)
        .writeTimeout(30, TimeUnit.SECONDS)
        .build()

    fun createSession(streamUrl: String, userAgent: String): SessionResponse {
        val payload = JSONObject()
            .put("url", streamUrl)
            .put("userAgent", userAgent)
            .toString()
        val reqBuilder = Request.Builder()
            .url(join(baseUrl, "/api/v1/sessions"))
            .post(payload.toRequestBody("application/json".toMediaType()))
        if (!accessSecret.isNullOrBlank()) {
            reqBuilder.header("X-Access-Secret", accessSecret)
        }
        client.newCall(reqBuilder.build()).execute().use { resp ->
            val body = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) {
                val detail = try {
                    val o = JSONObject(body)
                    listOfNotNull(o.optString("error"), o.optString("hint"))
                        .filter { it.isNotBlank() }
                        .joinToString(" — ")
                } catch (_: Exception) {
                    body.ifBlank { "HTTP ${resp.code}" }
                }
                throw IllegalStateException(detail.ifBlank { "HTTP ${resp.code}" })
            }
            return parseSession(body)
        }
    }

    fun absoluteUrl(path: String): String = join(baseUrl, path)

    fun wsAbsoluteUrl(path: String): String {
        val http = absoluteUrl(path)
        return when {
            http.startsWith("https://") -> "wss://" + http.removePrefix("https://")
            http.startsWith("http://") -> "ws://" + http.removePrefix("http://")
            else -> http
        }
    }

    private fun parseSession(body: String): SessionResponse {
        val json = JSONObject(body)
        val ice = mutableListOf<IceServerConfig>()
        val arr: JSONArray = json.optJSONArray("iceServers") ?: JSONArray()
        for (i in 0 until arr.length()) {
            val o = arr.getJSONObject(i)
            val urlsArr = o.getJSONArray("urls")
            val urls = mutableListOf<String>()
            for (j in 0 until urlsArr.length()) {
                urls.add(urlsArr.getString(j))
            }
            ice.add(
                IceServerConfig(
                    urls = urls,
                    username = if (o.has("username")) o.getString("username") else null,
                    credential = if (o.has("credential")) o.getString("credential") else null,
                )
            )
        }
        return SessionResponse(
            sessionId = json.getString("sessionId"),
            streamKey = json.getString("streamKey"),
            manifestUrl = json.getString("manifestUrl"),
            wsUrl = json.getString("wsUrl"),
            iceServers = ice,
        )
    }

    companion object {
        fun join(base: String, path: String): String {
            val b = base.trimEnd('/')
            val p = if (path.startsWith("/")) path else "/$path"
            return b + p
        }
    }
}
