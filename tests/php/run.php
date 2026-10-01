<?php

// Dependency-free tests for the plugin's pure logic: php tests/php/run.php

declare(strict_types=1);

require __DIR__ . '/../../plugin/src/UrlMapper.php';
require __DIR__ . '/../../plugin/src/MirrorDiscovery.php';

use Compost\MirrorDiscovery;
use Compost\UrlMapper;

$failures = 0;
function check(string $name, $got, $want): void
{
    global $failures;
    if ($got === $want) {
        echo "ok   $name\n";
        return;
    }
    $failures++;
    echo "FAIL $name\n     got:  " . var_export($got, true) . "\n     want: " . var_export($want, true) . "\n";
}

$mirrors = MirrorDiscovery::parse(json_encode(['mirrors' => [
    ['name' => 'packagist', 'path' => '/packagist/', 'upstream' => 'https://repo.packagist.org/'],
    ['name' => 'github-api', 'path' => '/github-api/', 'upstream' => 'https://api.github.com/repos/'],
    ['name' => 'broken'],
]]));
check('parse skips invalid entries', count($mirrors), 2);
check('parse rejects non-documents', MirrorDiscovery::parse('{"nope":1}'), null);

$mapper = new UrlMapper('https://compost.example.com/', $mirrors);
check(
    'metadata',
    $mapper->map('https://repo.packagist.org/p2/monolog/monolog.json'),
    'https://compost.example.com/packagist/p2/monolog/monolog.json'
);
check(
    'github zipball',
    $mapper->map('https://api.github.com/repos/Seldaek/monolog/zipball/abc123'),
    'https://compost.example.com/github-api/Seldaek/monolog/zipball/abc123'
);
check('unmapped host', $mapper->map('https://example.org/x.zip'), null);
check('other github api path', $mapper->map('https://api.github.com/user'), null);

$nested = new UrlMapper('http://proxy:8080', [
    ['upstream' => 'https://example.com/', 'path' => '/short/'],
    ['upstream' => 'https://example.com/deep/', 'path' => '/deep/'],
]);
check('longest upstream wins', $nested->map('https://example.com/deep/a.zip'), 'http://proxy:8080/deep/a.zip');

exit($failures === 0 ? 0 : 1);
