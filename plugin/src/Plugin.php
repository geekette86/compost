<?php

declare(strict_types=1);

namespace Compost;

use Composer\Composer;
use Composer\EventDispatcher\EventSubscriberInterface;
use Composer\IO\IOInterface;
use Composer\Plugin\Capability\CommandProvider as CommandProviderCapability;
use Composer\Plugin\Capable;
use Composer\Plugin\PluginEvents;
use Composer\Plugin\PluginInterface;
use Composer\Plugin\PreFileDownloadEvent;

/**
 * Sends Composer's metadata and dist downloads through a Compost proxy.
 *
 * URLs are rewritten only at download time, so composer.lock keeps the
 * original upstream URLs and works for people without the proxy.
 */
final class Plugin implements PluginInterface, EventSubscriberInterface, Capable
{
    /** @var Composer */
    private $composer;

    /** @var IOInterface */
    private $io;

    /** @var UrlMapper|null */
    private $mapper;

    /** @var bool */
    private $initialized = false;

    public function activate(Composer $composer, IOInterface $io): void
    {
        $this->composer = $composer;
        $this->io = $io;
    }

    public function deactivate(Composer $composer, IOInterface $io): void
    {
    }

    public function uninstall(Composer $composer, IOInterface $io): void
    {
    }

    public function getCapabilities(): array
    {
        return [CommandProviderCapability::class => CommandProvider::class];
    }

    public static function getSubscribedEvents(): array
    {
        return [PluginEvents::PRE_FILE_DOWNLOAD => ['onPreFileDownload', 0]];
    }

    public function onPreFileDownload(PreFileDownloadEvent $event): void
    {
        $mapper = $this->mapper();
        if ($mapper === null) {
            return;
        }
        $original = $event->getProcessedUrl();
        $mapped = $mapper->map($original);
        if ($mapped === null) {
            return;
        }
        $event->setProcessedUrl($mapped);
        // Keep Composer's local cache keyed on the upstream URL, so switching
        // the proxy on or off doesn't invalidate it.
        if ($event->getType() === 'package' && method_exists($event, 'setCustomCacheKey') && $event->getCustomCacheKey() === null) {
            $event->setCustomCacheKey($original);
        }
        if ($this->io->isDebug()) {
            $this->io->writeError(sprintf('[compost] %s -> %s', $original, $mapped));
        }
    }

    private function mapper(): ?UrlMapper
    {
        if ($this->initialized) {
            return $this->mapper;
        }
        $this->initialized = true;

        $config = $this->composer->getConfig();
        $settings = Settings::load($config);
        if (!$settings->enabled || $settings->url === null) {
            return null;
        }
        try {
            $mirrors = (new MirrorDiscovery($this->io, $config))->discover($settings->url);
        } catch (\Throwable $e) {
            $this->io->writeError(sprintf(
                '<warning>[compost] Proxy %s unavailable, downloading directly: %s</warning>',
                $settings->url,
                $e->getMessage()
            ));
            return null;
        }
        $this->mapper = new UrlMapper($settings->url, $mirrors);
        return $this->mapper;
    }
}
