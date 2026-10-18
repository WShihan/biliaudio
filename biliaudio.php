<!-- 所参考的php版本 -->
<?php
error_reporting(0);
header('Content-Type: text/html; charset=utf-8');

define('VIEW_API', 'https://api.bilibili.com/x/web-interface/view?');
define('PLAY_API', 'https://api.bilibili.com/x/player/playurl?fnval=16&fnver=0&fourk=1');
define('REFERER', 'https://www.bilibili.com/');
define('UA', 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36');

if (isset($_GET['proxy']) && isset($_GET['url']) && isset($_GET['filename'])) {
    $audioUrl = base64_decode($_GET['url']);
    $filename = base64_decode($_GET['filename']);
    
    $fallbackName = preg_replace('/[^\x20-\x7E]/', '', $filename);
    if (empty(trim($fallbackName))) {
        $fallbackName = 'bilibili_audio';
    }
    
    $headers =[
        'Referer: ' . REFERER,
        'Origin: https://www.bilibili.com',
        'User-Agent: ' . UA
    ];
    
    if (isset($_SERVER['HTTP_RANGE'])) {
        $headers[] = 'Range: ' . $_SERVER['HTTP_RANGE'];
    }

    $ch = curl_init();
    curl_setopt($ch, CURLOPT_URL, $audioUrl);
    curl_setopt($ch, CURLOPT_RETURNTRANSFER, false);
    curl_setopt($ch, CURLOPT_FOLLOWLOCATION, true);
    curl_setopt($ch, CURLOPT_SSL_VERIFYPEER, false);
    curl_setopt($ch, CURLOPT_TIMEOUT, 0);
    curl_setopt($ch, CURLOPT_HTTPHEADER, $headers);
    
    header('Content-Type: audio/mp4');
    header('Accept-Ranges: bytes');
    header('Cache-Control: public, max-age=31536000');
    header('Content-Disposition: attachment; filename="' . $fallbackName . '.m4a"; filename*=UTF-8\'\'' . rawurlencode($filename) . '.m4a');
    
    curl_setopt($ch, CURLOPT_HEADERFUNCTION, function($ch, $headerLine) {
        if (preg_match('/^HTTP\/(1\.0|1\.1|2|3) (\d+) /i', $headerLine, $matches)) {
            http_response_code(intval($matches[2]));
        } else {
            $headerParts = explode(':', $headerLine, 2);
            if (count($headerParts) == 2) {
                $headerName = strtolower(trim($headerParts[0]));
                if (in_array($headerName, ['content-length', 'content-range'])) {
                    header(trim($headerLine));
                }
            }
        }
        return strlen($headerLine);
    });
    
    curl_setopt($ch, CURLOPT_WRITEFUNCTION, function($ch, $data) {
        echo $data;
        if (ob_get_level() > 0) ob_flush();
        flush();
        return strlen($data);
    });
    
    curl_exec($ch);
    curl_close($ch);
    exit;
}

function fetch($url, $headers =[]) {
    $ch = curl_init();
    curl_setopt($ch, CURLOPT_URL, $url);
    curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
    curl_setopt($ch, CURLOPT_FOLLOWLOCATION, true);
    curl_setopt($ch, CURLOPT_SSL_VERIFYPEER, false);
    curl_setopt($ch, CURLOPT_TIMEOUT, 30);
    curl_setopt($ch, CURLOPT_HTTPHEADER, $headers);
    $response = curl_exec($ch);
    curl_close($ch);
    return $response;
}

function getRedirectUrl($url) {
    $ch = curl_init();
    curl_setopt($ch, CURLOPT_URL, $url);
    curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
    curl_setopt($ch, CURLOPT_FOLLOWLOCATION, false);
    curl_setopt($ch, CURLOPT_SSL_VERIFYPEER, false);
    curl_setopt($ch, CURLOPT_TIMEOUT, 30);
    curl_setopt($ch, CURLOPT_HEADER, true);
    curl_setopt($ch, CURLOPT_NOBODY, true);
    
    $response = curl_exec($ch);
    $httpCode = curl_getinfo($ch, CURLINFO_HTTP_CODE);
    curl_close($ch);
    
    if (in_array($httpCode,[301, 302, 307, 308])) {
        if (preg_match('/Location:\s*(.+?)\r?\n/i', $response, $matches)) {
            $redirectUrl = trim($matches[1]);
            if (strpos($redirectUrl, 'http') !== 0) {
                $parsed = parse_url($url);
                $redirectUrl = $parsed['scheme'] . '://' . $parsed['host'] . (strpos($redirectUrl, '/') === 0 ? '' : '/') . $redirectUrl;
            }
            return $redirectUrl;
        }
    }
    
    if ($httpCode == 200) {
        return $url;
    }
    
    return null;
}

function buildApiHeaders() {
    return[
        'Referer: ' . REFERER,
        'Origin: https://www.bilibili.com',
        'User-Agent: ' . UA
    ];
}

function extractVideoIds($input) {
    $results =[];
    
    $bvPattern = '/(?i)(BV[0-9A-Za-z]{10})/';
    $avPattern = '/(?i)(?<![0-9A-Za-z])av(\d+)(?![0-9A-Za-z])/';
    $b23Pattern = '/https?:\/\/b23\.tv\/[0-9A-Za-z]+/i';
    $biliUrlPattern = '/https?:\/\/(?:www\.)?bilibili\.com\/video\/[^\s]+/i';
    
    preg_match_all($bvPattern, $input, $bvMatches);
    foreach ($bvMatches[1] as $match) {
        $results[] = 'BV' . substr($match, 2);
    }
    
    preg_match_all($avPattern, $input, $avMatches);
    foreach ($avMatches[1] as $match) {
        $results[] = 'av' . $match;
    }
    
    preg_match_all($b23Pattern, $input, $b23Matches);
    foreach ($b23Matches[0] as $url) {
        $redirectUrl = getRedirectUrl($url);
        if ($redirectUrl) {
            $nested = extractVideoIds($redirectUrl);
            $results = array_merge($results, $nested);
        }
    }
    
    preg_match_all($biliUrlPattern, $input, $biliMatches);
    foreach ($biliMatches[0] as $url) {
        preg_match_all($bvPattern, $url, $urlBvMatches);
        foreach ($urlBvMatches[1] as $match) {
            $results[] = 'BV' . substr($match, 2);
        }
        preg_match_all($avPattern, $url, $urlAvMatches);
        foreach ($urlAvMatches[1] as $match) {
            $results[] = 'av' . $match;
        }
    }
    
    return array_unique($results);
}

function requestView($videoId) {
    $query = strpos($videoId, 'BV') === 0 
        ? 'bvid=' . urlencode($videoId)
        : 'aid=' . urlencode(substr($videoId, 2));
    $response = fetch(VIEW_API . $query, buildApiHeaders());
    $data = json_decode($response, true);
    if ($data['code'] !== 0) {
        throw new Exception($data['message'] ?? '接口返回异常');
    }
    return $data['data'];
}

function requestPlayUrl($bvid, $cid) {
    $api = PLAY_API . '&bvid=' . urlencode($bvid) . '&cid=' . urlencode($cid);
    $response = fetch($api, buildApiHeaders());
    $data = json_decode($response, true);
    if ($data['code'] !== 0) {
        throw new Exception($data['message'] ?? '接口返回异常');
    }
    return $data['data'];
}

function pickBestAudioUrl($playData) {
    if (!$playData || !isset($playData['dash']['audio'])) return null;
    $audios = $playData['dash']['audio'];
    if (empty($audios)) return null;
    
    $bestAudio = null;
    $maxBandwidth = -1;
    foreach ($audios as $audio) {
        $bandwidth = $audio['bandwidth'] ?? 0;
        if ($bestAudio === null || $bandwidth > $maxBandwidth) {
            $bestAudio = $audio;
            $maxBandwidth = $bandwidth;
        }
    }
    
    if (!$bestAudio) return null;
    
    $url = $bestAudio['base_url'] ?? $bestAudio['baseUrl'] ?? '';
    if (!empty($url)) return $url;
    
    $backupUrls = $bestAudio['backup_url'] ?? $bestAudio['backupUrl'] ??[];
    if (!empty($backupUrls)) return $backupUrls[0];
    
    return null;
}

function formatDuration($seconds) {
    $m = floor($seconds / 60);
    $s = $seconds % 60;
    return sprintf('%d:%02d', $m, $s);
}

function formatNumber($num) {
    if ($num >= 10000) {
        return round($num / 10000, 1) . '万';
    }
    return $num;
}

function sanitizeFilename($str) {
    $str = preg_replace('/[^\x{4e00}-\x{9fa5}a-zA-Z0-9\s\-_\(\)\[\]【】]/u', '_', $str);
    if ($str === null) {
        return 'bilibili_audio_' . time();
    }
    $str = preg_replace('/_+/', '_', $str);
    $str = trim($str, '_');
    return $str ?: 'bilibili_audio_' . time();
}

function truncate($str, $length = 120, $suffix = '...') {
    if (mb_strlen($str, 'UTF-8') <= $length) return $str;
    return mb_substr($str, 0, $length, 'UTF-8') . $suffix;
}

$videos = [];
$globalAudioList =[];
$error = '';
$input = '';

if ($_SERVER['REQUEST_METHOD'] === 'POST' && !empty($_POST['input'])) {
    $input = $_POST['input'];
    $videoIds = extractVideoIds($input);
    
    if (empty($videoIds)) {
        $error = '未识别到有效的内容';
    } else {
        foreach ($videoIds as $vid) {
            try {
                $viewData = requestView($vid);
                $bvid = $viewData['bvid'];
                $title = $viewData['title'];
                $author = $viewData['owner']['name'] ?? '未知UP';
                $cover = $viewData['pic'];
                $duration = $viewData['duration'];
                $stat = $viewData['stat'] ??[];
                $desc = $viewData['desc'] ?? '';
                $pubdate = $viewData['pubdate'] ?? 0;
                $pages = $viewData['pages'] ?? [];
                
                $videoInfo =[
                    'bvid' => $bvid,
                    'title' => $title,
                    'author' => $author,
                    'cover' => $cover,
                    'duration' => formatDuration($duration),
                    'view' => formatNumber($stat['view'] ?? 0),
                    'like' => formatNumber($stat['like'] ?? 0),
                    'coin' => formatNumber($stat['coin'] ?? 0),
                    'favorite' => formatNumber($stat['favorite'] ?? 0),
                    'share' => formatNumber($stat['share'] ?? 0),
                    'danmaku' => formatNumber($stat['danmaku'] ?? 0),
                    'desc' => $desc,
                    'pubdate' => $pubdate,
                    'parts' =>[]
                ];
                
                if (count($pages) > 1) {
                    foreach ($pages as $index => $page) {
                        $cid = $page['cid'];
                        $partName = $page['part'] ?: ('P' . ($index + 1));
                        $pageNum = $page['page'] ?? ($index + 1);
                        $displayName = 'P' . $pageNum . ($page['part'] ? ' - ' . $page['part'] : '');
                        
                        $filename = sanitizeFilename($displayName . ' - ' . $title);
                        
                        try {
                            $playData = requestPlayUrl($bvid, $cid);
                            $audioUrl = pickBestAudioUrl($playData);
                            if ($audioUrl) {
                                $proxyUrl = '?proxy=1&url=' . urlencode(base64_encode($audioUrl)) . '&filename=' . urlencode(base64_encode($filename));
                                $part =[
                                    'name' => $displayName,
                                    'cid' => $cid,
                                    'audioUrl' => $audioUrl,
                                    'proxyUrl' => $proxyUrl,
                                    'duration' => formatDuration($page['duration']),
                                    'filename' => $filename
                                ];
                                $videoInfo['parts'][] = $part;
                                
                                $globalAudioList[] =[
                                    'name' => $displayName . ' - ' . $title,
                                    'artist' => $author,
                                    'url' => $proxyUrl,
                                    'cover' => $cover,
                                    'duration' => $part['duration'],
                                    'bvid' => $bvid,
                                    'partName' => $displayName
                                ];
                            }
                        } catch (Exception $e) {
                            continue;
                        }
                    }
                } else {
                    $cid = $viewData['cid'];
                    $displayName = '完整音频';
                    
                    $filename = sanitizeFilename($title);
                    
                    try {
                        $playData = requestPlayUrl($bvid, $cid);
                        $audioUrl = pickBestAudioUrl($playData);
                        if ($audioUrl) {
                            $proxyUrl = '?proxy=1&url=' . urlencode(base64_encode($audioUrl)) . '&filename=' . urlencode(base64_encode($filename));
                            $part =[
                                'name' => $displayName,
                                'cid' => $cid,
                                'audioUrl' => $audioUrl,
                                'proxyUrl' => $proxyUrl,
                                'duration' => formatDuration($duration),
                                'filename' => $filename
                            ];
                            $videoInfo['parts'][] = $part;
                            
                            $globalAudioList[] =[
                                'name' => $title,
                                'artist' => $author,
                                'url' => $proxyUrl,
                                'cover' => $cover,
                                'duration' => $part['duration'],
                                'bvid' => $bvid,
                                'partName' => $displayName
                            ];
                        }
                    } catch (Exception $e) {
                        continue;
                    }
                }
                
                if (!empty($videoInfo['parts'])) {
                    $videos[] = $videoInfo;
                }
            } catch (Exception $e) {
                continue;
            }
        }
        
        if (empty($videos)) {
            $error = '未能获取到任何视频信息';
        }
    }
}
?>
<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <meta name="referrer" content="no-referrer">
    <title>哔哩哔哩音频解析</title>
    <link rel="stylesheet" href="https://jsd2.pixcat.cn/npm/aplayer@1.10.1/dist/APlayer.min.css">
    <link rel="stylesheet" href="https://jsd2.pixcat.cn/npm/@fortawesome/fontawesome-free@6.0.0/css/all.min.css">
    <style>
        :root {
            --primary: #00AEEC;
            --primary-hover: #009ce0;
            --primary-gradient: linear-gradient(135deg, #00AEEC, #2979ff);
            --bg-body: #f1f5f9;
            --card-bg: #ffffff;
            --text-main: #0f172a;
            --text-muted: #64748b;
            --border-color: #e2e8f0;
            --shadow-sm: 0 1px 2px 0 rgb(0 0 0 / 0.05);
            --shadow-md: 0 4px 6px -1px rgb(0 0 0 / 0.05), 0 2px 4px -2px rgb(0 0 0 / 0.05);
            --shadow-lg: 0 10px 25px -5px rgb(0 0 0 / 0.08), 0 8px 10px -6px rgb(0 0 0 / 0.04);
            --radius-xl: 20px;
            --radius-lg: 14px;
            --radius-full: 9999px;
            --transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
        }
        
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        
        body {
            font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif;
            background: var(--bg-body);
            color: var(--text-main);
            line-height: 1.6;
            padding: 4rem 1rem 3rem;
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
        }
        
        .container {
            max-width: 1000px;
            margin: 0 auto;
            display: flex;
            flex-direction: column;
            width: 100%;
        }
        
        .hero {
            text-align: center;
            margin-bottom: 2.5rem;
        }
        
        .hero h1 {
            font-size: 2.4rem;
            font-weight: 800;
            margin-bottom: 0.8rem;
            display: inline-flex;
            align-items: center;
            gap: 0.6rem;
            background: var(--primary-gradient);
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
            letter-spacing: -0.5px;
        }

        .hero h1 i {
            -webkit-text-fill-color: var(--primary);
        }
        
        .hero p {
            color: var(--text-muted);
            font-size: 1.05rem;
            font-weight: 500;
        }
        
        .search-card {
            background: var(--card-bg);
            border-radius: var(--radius-xl);
            box-shadow: var(--shadow-lg);
            padding: 1.8rem;
            margin-bottom: 2.5rem;
            border: 1px solid rgba(255,255,255,0.8);
        }
        
        textarea {
            width: 100%;
            padding: 1.2rem;
            border: 1px solid var(--border-color);
            border-radius: var(--radius-lg);
            font-size: 1rem;
            font-family: inherit;
            resize: vertical;
            transition: var(--transition);
            background: #f8fafc;
            color: var(--text-main);
            line-height: 1.5;
        }
        
        textarea:focus {
            outline: none;
            background: #ffffff;
            border-color: var(--primary);
            box-shadow: 0 0 0 4px rgba(0, 174, 236, 0.15);
        }
        
        #submitBtn {
            background: var(--primary-gradient);
            color: #ffffff;
            border: none;
            padding: 0.8rem 2rem;
            border-radius: var(--radius-full);
            font-size: 1.05rem;
            font-weight: 600;
            cursor: pointer;
            transition: var(--transition);
            display: inline-flex;
            align-items: center;
            gap: 0.6rem;
            margin-top: 1.2rem;
            box-shadow: 0 4px 12px rgba(0, 174, 236, 0.3);
        }
        
        #submitBtn:hover {
            transform: translateY(-2px);
            box-shadow: 0 6px 16px rgba(0, 174, 236, 0.4);
        }

        #submitBtn:active {
            transform: translateY(0);
        }
        
        .hint {
            font-size: 0.85rem;
            color: var(--text-muted);
            font-weight: 500;
            margin-top: 1.2rem;
            display: flex;
            align-items: center;
            gap: 0.4rem;
        }
        
        @keyframes shake {
            0%, 100% { transform: translateX(0); }
            20%, 60% { transform: translateX(-5px); }
            40%, 80% { transform: translateX(5px); }
        }

        .global-player-card {
            background: var(--card-bg);
            border-radius: var(--radius-xl);
            box-shadow: var(--shadow-lg);
            margin-top: 2rem;
            overflow: hidden;
            order: 2;
            border: 1px solid var(--border-color);
        }
        
        .aplayer {
            border-radius: 0;
            box-shadow: none;
            background: transparent;
            margin: 0;
        }
        
        .videos-container {
            order: 1;
        }
        
        .video-card {
            background: var(--card-bg);
            border-radius: var(--radius-xl);
            box-shadow: var(--shadow-lg);
            margin-bottom: 2rem;
            overflow: hidden;
            transition: var(--transition);
            border: 1px solid rgba(255,255,255,0.8);
        }
        
        .video-card:hover {
            transform: translateY(-4px);
            box-shadow: 0 20px 25px -5px rgb(0 0 0 / 0.1), 0 8px 10px -6px rgb(0 0 0 / 0.05);
        }
        
        .video-header {
            display: flex;
            gap: 1.5rem;
            padding: 1.5rem;
            border-bottom: 1px solid var(--border-color);
            background: #ffffff;
        }
        
        .cover {
            width: 180px;
            height: 101px;
            object-fit: cover;
            border-radius: var(--radius-lg);
            flex-shrink: 0;
            background: var(--bg-body);
            box-shadow: var(--shadow-sm);
        }
        
        .video-info {
            flex: 1;
            min-width: 200px;
        }
        
        .video-title {
            font-size: 1.25rem;
            font-weight: 700;
            margin-bottom: 0.5rem;
            line-height: 1.4;
            color: var(--text-main);
        }
        
        .video-meta {
            display: flex;
            flex-wrap: wrap;
            gap: 1rem;
            font-size: 0.85rem;
            font-weight: 500;
            color: var(--text-muted);
            margin-bottom: 0.6rem;
        }
        
        .video-meta span {
            display: inline-flex;
            align-items: center;
            gap: 0.4rem;
        }
        
        .video-stats {
            display: flex;
            flex-wrap: wrap;
            gap: 1.2rem;
            margin: 0.8rem 0 0.5rem;
            font-size: 0.85rem;
            font-weight: 500;
            color: var(--text-muted);
        }
        
        .video-stats i {
            color: var(--primary);
            opacity: 0.8;
        }
        
        .desc {
            font-size: 0.85rem;
            color: var(--text-muted);
            background: #f8fafc;
            padding: 0.8rem 1rem;
            border-radius: var(--radius-lg);
            margin-top: 1rem;
            line-height: 1.6;
            word-break: break-word;
            border: 1px solid var(--border-color);
        }
        
        .parts {
            padding: 0.5rem 1.5rem 1.5rem;
            background: #ffffff;
        }
        
        .part {
            display: flex;
            align-items: center;
            justify-content: space-between;
            padding: 1rem 0;
            border-bottom: 1px solid var(--border-color);
            flex-wrap: wrap;
            gap: 1rem;
        }
        
        .part:last-child {
            border-bottom: none;
            padding-bottom: 0;
        }
        
        .part-name {
            font-size: 0.95rem;
            font-weight: 600;
            display: flex;
            align-items: center;
            gap: 0.6rem;
            color: var(--text-main);
            line-height: 1.4;
        }
        
        .part-name i {
            color: var(--primary);
        }
        
        .part-time {
            font-size: 0.8rem;
            font-weight: 600;
            color: var(--text-muted);
            background: #f1f5f9;
            padding: 0.2rem 0.6rem;
            border-radius: var(--radius-full);
        }
        
        .part-actions {
            display: flex;
            gap: 0.8rem;
        }
        
        .btn-icon {
            padding: 0.5rem 1.2rem;
            border-radius: var(--radius-full);
            font-size: 0.85rem;
            font-weight: 600;
            display: inline-flex;
            align-items: center;
            gap: 0.4rem;
            transition: var(--transition);
            cursor: pointer;
            text-decoration: none;
            border: none;
        }
        
        .btn-play {
            background: var(--primary);
            color: #ffffff;
            box-shadow: 0 2px 8px rgba(0, 174, 236, 0.3);
        }
        
        .btn-play:hover {
            background: var(--primary-hover);
            transform: translateY(-1px);
            box-shadow: 0 4px 12px rgba(0, 174, 236, 0.4);
        }
        
        .btn-dl {
            background: #f1f5f9;
            color: var(--text-main);
        }
        
        .btn-dl:hover {
            background: #e2e8f0;
            transform: translateY(-1px);
        }
        
        .empty-state {
            text-align: center;
            padding: 4rem 1rem;
            color: var(--text-muted);
            font-weight: 500;
            font-size: 1.05rem;
            background: var(--card-bg);
            border-radius: var(--radius-xl);
            box-shadow: var(--shadow-lg);
            border: 1px solid rgba(255,255,255,0.8);
            margin-bottom: 2rem;
        }

        .empty-state i {
            display: block;
            font-size: 2rem;
            margin-bottom: 1rem;
            color: #cbd5e1;
        }
        
        .tools-section {
            order: 3;
            margin-top: 3rem;
            width: 100%;
        }

        .tools-details {
            background: var(--card-bg);
            border-radius: var(--radius-xl);
            box-shadow: var(--shadow-md);
            border: 1px solid var(--border-color);
            overflow: hidden;
            transition: var(--transition);
        }

        .tools-summary {
            padding: 1.5rem;
            cursor: pointer;
            font-weight: 700;
            font-size: 1.1rem;
            color: var(--text-main);
            display: flex;
            align-items: center;
            gap: 0.6rem;
            user-select: none;
            background: #ffffff;
            transition: var(--transition);
        }

        .tools-summary .arrow-icon {
            margin-left: auto;
            color: var(--text-muted);
            transition: transform 0.4s cubic-bezier(0.4, 0, 0.2, 1);
        }

        .tools-wrapper {
            display: grid;
            grid-template-rows: 0fr;
            transition: grid-template-rows 0.4s cubic-bezier(0.4, 0, 0.2, 1);
            background: #ffffff;
        }

        .tools-inner {
            overflow: hidden;
        }

        .tools-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
            gap: 1.2rem;
            padding: 0 1.5rem 1.5rem 1.5rem;
        }
        
        .tools-details.is-open .tools-summary {
            border-bottom: 1px solid var(--border-color);
            background: #f8fafc;
        }

        .tools-details.is-open .arrow-icon {
            transform: rotate(180deg);
        }

        .tools-details.is-open .tools-wrapper {
            grid-template-rows: 1fr;
        }
        
        .tool-card {
            background: #f8fafc;
            border-radius: var(--radius-lg);
            padding: 1.2rem;
            text-decoration: none;
            color: inherit;
            border: 1px solid var(--border-color);
            transition: var(--transition);
            display: flex;
            flex-direction: column;
            gap: 0.5rem;
        }
        
        .tool-card:hover {
            background: #ffffff;
            border-color: var(--primary);
            box-shadow: var(--shadow-md);
            transform: translateY(-2px);
        }
        
        .tool-title {
            font-size: 1rem;
            font-weight: 700;
            color: var(--text-main);
        }
        
        .tool-desc {
            font-size: 0.85rem;
            color: var(--text-muted);
            line-height: 1.5;
        }
        
        .site-footer {
            order: 4;
            margin-top: 4rem;
            text-align: center;
            font-size: 0.9rem;
            font-weight: 500;
            color: var(--text-muted);
            padding-top: 1.5rem;
        }
        
        .site-footer a {
            color: var(--text-muted);
            text-decoration: none;
            transition: color 0.2s;
            font-weight: 600;
        }
        
        .site-footer a:hover {
            color: var(--primary);
        }
        
        @media (max-width: 680px) {
            body {
                padding: 2rem 1rem;
            }
            .video-header {
                flex-direction: column;
                gap: 1rem;
            }
            .cover {
                width: 100%;
                height: auto;
                aspect-ratio: 16/9;
            }
            .part {
                flex-direction: column;
                align-items: flex-start;
            }
            .part-actions {
                width: 100%;
            }
            .btn-icon {
                flex: 1;
                justify-content: center;
            }
            .video-stats {
                gap: 0.8rem;
            }
            .tools-grid {
                grid-template-columns: 1fr;
            }
        }
        
        @media (max-width: 480px) {
            .hero h1 {
                font-size: 1.8rem;
            }
            .video-title {
                font-size: 1.1rem;
            }
            .tools-summary {
                font-size: 1rem;
            }
        }
    </style>
</head>
<body>
<div class="container">
    <div class="hero">
        <h1><i class="fa-brands fa-bilibili"></i> 哔哩哔哩音频解析</h1>
        <p>获取指定视频的音频部分，并尝试以高音质下载</p>
    </div>
    
    <div class="search-card">
        <form method="POST" id="form">
            <div class="input-group">
                <textarea name="input" placeholder="填入粘贴 BV/AV 号、b23.tv 短链、B站完整分享链接或包含它们的文本..."><?php echo htmlspecialchars($input); ?></textarea>
            </div>
            <button type="submit" id="submitBtn"><i class="fas fa-search"></i> 解析</button>
            <div class="hint"><i class="fas fa-lightbulb"></i>支持分P与多个视频条目</div>
        </form>
    </div>
    
    <?php if (empty($globalAudioList)): ?>
        <?php if ($error): ?>
            <div id="status-box" class="empty-state">
                <i class="fas fa-exclamation-circle" style="color: #ef4444;"></i>
                <span><?php echo $error; ?></span>
            </div>
        <?php elseif ($_SERVER['REQUEST_METHOD'] === 'POST'): ?>
            <div id="status-box" class="empty-state">
                <i class="fas fa-search"></i>
                <span>未找到有效音频，请检查输入内容</span>
            </div>
        <?php else: ?>
            <div id="status-box" class="empty-state">
                <i class="fas fa-inbox"></i>
                <span>在输入框中粘贴 B 站链接开始解析</span>
            </div>
        <?php endif; ?>
    <?php endif; ?>
    
    <?php if (!empty($globalAudioList)): ?>
        <div class="videos-container">
            <?php foreach ($videos as $video): ?>
                <div class="video-card" data-bvid="<?php echo $video['bvid']; ?>">
                    <div class="video-header">
                        <img class="cover" src="<?php echo htmlspecialchars($video['cover']); ?>" alt="封面" loading="lazy">
                        <div class="video-info">
                            <div class="video-title"><?php echo htmlspecialchars($video['title']); ?></div>
                            <div class="video-meta">
                                <span><i class="fas fa-user-circle"></i> <?php echo htmlspecialchars($video['author']); ?></span>
                                <span><i class="far fa-calendar-alt"></i> <?php echo date('Y-m-d', $video['pubdate']); ?></span>
                                <span><i class="far fa-clock"></i> <?php echo $video['duration']; ?></span>
                            </div>
                            <div class="video-stats">
                                <span><i class="fas fa-play"></i> <?php echo $video['view']; ?></span>
                                <span><i class="fas fa-thumbs-up"></i> <?php echo $video['like']; ?></span>
                                <span><i class="fas fa-coins"></i> <?php echo $video['coin']; ?></span>
                                <span><i class="fas fa-star"></i> <?php echo $video['favorite']; ?></span>
                                <span><i class="fas fa-share-alt"></i> <?php echo $video['share']; ?></span>
                                <span><i class="fas fa-comment-dots"></i> <?php echo $video['danmaku']; ?></span>
                            </div>
                            <?php if (!empty($video['desc'])): ?>
                                <div class="desc"><i class="fas fa-align-left"></i> <?php echo nl2br(htmlspecialchars(truncate($video['desc'], 180))); ?></div>
                            <?php endif; ?>
                        </div>
                    </div>
                    
                    <div class="parts">
                        <?php foreach ($video['parts'] as $index => $part): ?>
                            <div class="part" data-part-index="<?php echo $index; ?>">
                                <div class="part-name">
                                    <i class="fas fa-music"></i> <?php echo htmlspecialchars($part['name']); ?>
                                    <span class="part-time"><?php echo $part['duration']; ?></span>
                                </div>
                                <div class="part-actions">
                                    <button class="btn-icon btn-play" data-audio-name="<?php echo htmlspecialchars(count($video['parts']) > 1 ? $part['name'] . ' - ' . $video['title'] : $video['title'], ENT_QUOTES); ?>" data-artist="<?php echo htmlspecialchars($video['author'], ENT_QUOTES); ?>" data-cover="<?php echo htmlspecialchars($video['cover']); ?>" data-url="<?php echo $part['proxyUrl']; ?>"><i class="fas fa-play"></i> 播放</button>
                                    <a href="<?php echo $part['proxyUrl']; ?>" class="btn-icon btn-dl" download="<?php echo htmlspecialchars($part['filename'], ENT_QUOTES); ?>.m4a"><i class="fas fa-download"></i> 下载</a>
                                </div>
                            </div>
                        <?php endforeach; ?>
                    </div>
                </div>
            <?php endforeach; ?>
        </div>
        
        <div class="global-player-card">
            <div id="global-player"></div>
        </div>
    <?php endif; ?>
    
    <div class="tools-section">
        <div class="tools-details" id="toolsContainer">
            <div class="tools-summary" id="toolsSummary">
                <i class="fas fa-layer-group"></i> 相关工具收集
                <i class="fas fa-chevron-down arrow-icon"></i>
            </div>
            <div class="tools-wrapper">
                <div class="tools-inner">
                    <div class="tools-grid">
                        <a href="https://bili23.scott-sloan.cn/" target="_blank" class="tool-card">
                            <div class="tool-title">Bili23 Downloader</div>
                            <div class="tool-desc">支持现代 UI、音视频流分离下载、多线程下载、弹幕与字幕获取、封面提取及元数据刮削，可自定义文件命名与分类，兼容 Windows（含 Win7）、Linux 和 macOS 系统。</div>
                        </a>
                        <a href="https://github.com/BilibiliVideoDownload/BilibiliVideoDownload" target="_blank" class="tool-card">
                            <div class="tool-title">BilibiliVideoDownload</div>
                            <div class="tool-desc">开源的跨平台 B 站视频下载工具，基于 .NET / Avalonia 构建，支持登录账号、批量下载、8K / HDR 高画质、弹幕 / 字幕 / 封面导出与音视频分离，可自定义任务并发与下载目录，适配 Windows / Linux / macOS。</div>
                        </a>
                        <a href="https://www.jijidown.com/" target="_blank" class="tool-card">
                            <div class="tool-title">哔哩哔哩唧唧</div>
                            <div class="tool-desc">经典的 B 站视频与弹幕下载工具</div>
                        </a>
                        <a href="https://vidbee.org/" target="_blank" class="tool-card">
                            <div class="tool-title">VidBee</div>
                            <div class="tool-desc">基于 yt-dlp 的全能视频下载助手，提供广泛的平台支持</div>
                        </a>
                        <a href="https://github.com/yqjiangxin/awesome-bilibili-extra" target="_blank" class="tool-card">
                            <div class="tool-title">B站扩展脚本合集</div>
                            <div class="tool-desc">收集整理能让浏览 B 站时体验更加方便的浏览器扩展与脚本</div>
                        </a>
                    </div>
                </div>
            </div>
        </div>
    </div>

    <footer class="site-footer">
        &copy; <a href="https://www.pixcat.cn/lab/" target="_blank">烂活实验室</a>
    </footer>

</div>

<script src="https://jsd2.pixcat.cn/npm/aplayer@1.10.1/dist/APlayer.min.js"></script>
<script>
    if (window.history && window.history.replaceState) {
        window.history.replaceState(null, null, window.location.href);
    }

    var globalAudioData = <?php echo json_encode($globalAudioList); ?>;
    var globalPlayer = null;

    function initGlobalPlayer() {
        if (!globalAudioData.length) return;
        globalPlayer = new APlayer({
            container: document.getElementById('global-player'),
            audio: globalAudioData.map(item => ({
                name: item.name,
                artist: item.artist,
                url: item.url,
                cover: item.cover,
                theme: '#00AEEC'
            })),
            autoplay: false,
            loop: 'all',
            order: 'list',
            listFolded: false,
            listMaxHeight: 280,
            theme: '#00AEEC'
        });
    }

    function playAudioInGlobal(audioName, artist, cover, url) {
        if (!globalPlayer) return;
        let index = -1;
        for (let i = 0; i < globalAudioData.length; i++) {
            if (globalAudioData[i].url === url) {
                index = i;
                break;
            }
        }
        if (index !== -1) {
            globalPlayer.list.switch(index);
            globalPlayer.play();
            document.querySelector('.global-player-card').scrollIntoView({ behavior: 'smooth', block: 'start' });
        } else {
            for (let i = 0; i < globalAudioData.length; i++) {
                if (globalAudioData[i].name === audioName && globalAudioData[i].artist === artist) {
                    globalPlayer.list.switch(i);
                    globalPlayer.play();
                    document.querySelector('.global-player-card').scrollIntoView({ behavior: 'smooth', block: 'start' });
                    break;
                }
            }
        }
    }

    function bindPlayButtons() {
        document.querySelectorAll('.btn-play').forEach(btn => {
            btn.removeEventListener('click', playHandler);
            btn.addEventListener('click', playHandler);
        });
    }

    function playHandler(e) {
        e.preventDefault();
        const name = this.dataset.audioName;
        const artist = this.dataset.artist;
        const cover = this.dataset.cover;
        const url = this.dataset.url;
        if (name && artist && url) {
            playAudioInGlobal(name, artist, cover, url);
        }
    }

    document.addEventListener('DOMContentLoaded', function() {
        if (globalAudioData.length) {
            initGlobalPlayer();
            bindPlayButtons();
        }
        
        const toolsSummary = document.getElementById('toolsSummary');
        if (toolsSummary) {
            toolsSummary.addEventListener('click', function() {
                document.getElementById('toolsContainer').classList.toggle('is-open');
            });
        }
    });

    const form = document.getElementById('form');
    if (form) {
        form.addEventListener('submit', function(e) {
            const textarea = document.querySelector('textarea[name="input"]');
            if (!textarea.value.trim()) {
                e.preventDefault();
                
                const videoContainer = document.querySelector('.videos-container');
                if (videoContainer) videoContainer.style.display = 'none';
                const playerCard = document.querySelector('.global-player-card');
                if (playerCard) playerCard.style.display = 'none';
                
                let statusBox = document.getElementById('status-box');
                if (!statusBox) {
                    statusBox = document.createElement('div');
                    statusBox.id = 'status-box';
                    const searchCard = document.querySelector('.search-card');
                    searchCard.parentNode.insertBefore(statusBox, searchCard.nextSibling);
                }
                
                statusBox.className = 'empty-state';
                statusBox.innerHTML = '<i class="fas fa-exclamation-circle" style="color: #ef4444;"></i> <span>请输入有效的视频链接或文本</span>';
                statusBox.style.display = 'block';
                
                statusBox.style.animation = 'none';
                statusBox.offsetHeight; 
                statusBox.style.animation = 'shake 0.4s ease-in-out';
                return;
            }
            const btn = document.getElementById('submitBtn');
            btn.disabled = true;
            btn.innerHTML = '<i class="fas fa-spinner fa-pulse"></i> 解析中...';
        });
    }
</script>
</body>
</html>
