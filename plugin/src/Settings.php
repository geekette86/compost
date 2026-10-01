<?php

declare(strict_types=1);

namespace Compost;

use Composer\Config;

/**
 * Plugin settings, stored in $COMPOSER_HOME/compost.json.
 *
 * The COMPOST_URL environment variable overrides the file and enables the
 * plugin; COMPOST_DISABLE=1 turns it off regardless of the file.
 */
final class Settings
{
    /** @var bool */
    public $enabled = false;

    /** @var string|null */
    public $url;

    public static function path(Config $config): string
    {
        return rtrim((string) $config->get('home'), '/') . '/compost.json';
    }

    public static function load(Config $config): self
    {
        $settings = new self();
        $file = self::path($config);
        if (is_file($file)) {
            $data = json_decode((string) file_get_contents($file), true);
            if (is_array($data)) {
                $settings->enabled = (bool) ($data['enabled'] ?? false);
                $settings->url = isset($data['url']) && is_string($data['url']) ? $data['url'] : null;
            }
        }

        $envUrl = getenv('COMPOST_URL');
        if (is_string($envUrl) && $envUrl !== '') {
            $settings->enabled = true;
            $settings->url = $envUrl;
        }
        $disable = getenv('COMPOST_DISABLE');
        if (is_string($disable) && $disable !== '' && $disable !== '0') {
            $settings->enabled = false;
        }
        return $settings;
    }

    public function save(Config $config): void
    {
        $file = self::path($config);
        if (!is_dir(dirname($file))) {
            mkdir(dirname($file), 0777, true);
        }
        $json = json_encode(['enabled' => $this->enabled, 'url' => $this->url], JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES);
        if (file_put_contents($file, $json . "\n") === false) {
            throw new \RuntimeException('Could not write ' . $file);
        }
    }
}
