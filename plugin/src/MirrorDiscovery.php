<?php

declare(strict_types=1);

namespace Compost;

use Composer\Config;
use Composer\Factory;
use Composer\IO\IOInterface;

/**
 * Fetches a proxy's /mirrors.json, caching it in Composer's cache directory so
 * normal Composer runs don't pay an extra request every time.
 */
final class MirrorDiscovery
{
    private const CACHE_TTL = 3600;

    /** @var IOInterface */
    private $io;

    /** @var Config */
    private $config;

    public function __construct(IOInterface $io, Config $config)
    {
        $this->io = $io;
        $this->config = $config;
    }

    /**
     * @return list<array{upstream: string, path: string}>
     */
    public function discover(string $baseUrl, bool $refresh = false): array
    {
        $cacheFile = $this->cacheFile($baseUrl);
        if (!$refresh && is_file($cacheFile) && filemtime($cacheFile) > time() - self::CACHE_TTL) {
            $mirrors = self::parse((string) file_get_contents($cacheFile));
            if ($mirrors !== null) {
                return $mirrors;
            }
        }

        $url = rtrim($baseUrl, '/') . '/mirrors.json';
        $body = (string) Factory::createHttpDownloader($this->io, $this->config)->get($url)->getBody();
        $mirrors = self::parse($body);
        if ($mirrors === null) {
            throw new \RuntimeException($url . ' is not a Compost discovery document');
        }
        if (is_dir(dirname($cacheFile)) || @mkdir(dirname($cacheFile), 0777, true)) {
            @file_put_contents($cacheFile, $body);
        }
        return $mirrors;
    }

    public function forget(string $baseUrl): void
    {
        @unlink($this->cacheFile($baseUrl));
    }

    /**
     * @return list<array{upstream: string, path: string}>|null
     */
    public static function parse(string $json): ?array
    {
        $data = json_decode($json, true);
        if (!is_array($data) || !isset($data['mirrors']) || !is_array($data['mirrors'])) {
            return null;
        }
        $mirrors = [];
        foreach ($data['mirrors'] as $mirror) {
            if (is_array($mirror) && is_string($mirror['upstream'] ?? null) && is_string($mirror['path'] ?? null)) {
                $mirrors[] = ['upstream' => $mirror['upstream'], 'path' => $mirror['path']];
            }
        }
        return $mirrors;
    }

    private function cacheFile(string $baseUrl): string
    {
        return rtrim((string) $this->config->get('cache-dir'), '/') . '/compost/mirrors-' . sha1($baseUrl) . '.json';
    }
}
