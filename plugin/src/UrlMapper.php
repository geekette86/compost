<?php

declare(strict_types=1);

namespace Compost;

/**
 * Rewrites upstream URLs to their Compost proxy equivalents.
 */
final class UrlMapper
{
    /** @var string */
    private $baseUrl;

    /** @var list<array{upstream: string, path: string}> */
    private $mirrors;

    /**
     * @param list<array{upstream: string, path: string}> $mirrors
     */
    public function __construct(string $baseUrl, array $mirrors)
    {
        $this->baseUrl = rtrim($baseUrl, '/');
        // Longest upstream first, so the most specific mirror wins.
        usort($mirrors, static function (array $a, array $b): int {
            return strlen($b['upstream']) <=> strlen($a['upstream']);
        });
        $this->mirrors = $mirrors;
    }

    /**
     * Returns the proxied URL, or null when no mirror covers $url.
     */
    public function map(string $url): ?string
    {
        foreach ($this->mirrors as $mirror) {
            $upstream = $mirror['upstream'];
            if (strncmp($url, $upstream, strlen($upstream)) === 0) {
                return $this->baseUrl . '/' . trim($mirror['path'], '/') . '/' . substr($url, strlen($upstream));
            }
        }
        return null;
    }
}
