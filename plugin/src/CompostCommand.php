<?php

declare(strict_types=1);

namespace Compost;

use Composer\Command\BaseCommand;
use Composer\Factory;
use Symfony\Component\Console\Input\InputArgument;
use Symfony\Component\Console\Input\InputInterface;
use Symfony\Component\Console\Output\OutputInterface;

/**
 * composer compost enable <url> | disable | status
 */
final class CompostCommand extends BaseCommand
{
    protected function configure(): void
    {
        $this->setName('compost')
            ->setDescription('Enable, disable or inspect the Compost download proxy')
            ->addArgument('action', InputArgument::REQUIRED, 'enable, disable or status')
            ->addArgument('url', InputArgument::OPTIONAL, 'Proxy base URL (for enable)')
            ->setHelp(<<<'HELP'
Routes Composer downloads through a Compost caching proxy.

  <info>composer compost enable https://compost.example.com</info>
  <info>composer compost status</info>
  <info>composer compost disable</info>

Settings are stored globally in $COMPOSER_HOME/compost.json. The COMPOST_URL
environment variable overrides them; COMPOST_DISABLE=1 switches the plugin off.
HELP
            );
    }

    protected function execute(InputInterface $input, OutputInterface $output): int
    {
        $io = $this->getIO();
        // Settings are global, so this works with or without a composer.json.
        $config = Factory::createConfig($io);
        $settings = Settings::load($config);
        $discovery = new MirrorDiscovery($io, $config);

        switch ($input->getArgument('action')) {
            case 'enable':
                $url = $input->getArgument('url');
                if (!is_string($url) || !preg_match('#^https?://#', $url)) {
                    $io->writeError('<error>Usage: composer compost enable <url> (http:// or https://)</error>');
                    return 1;
                }
                $url = rtrim($url, '/');
                try {
                    $mirrors = $discovery->discover($url, true);
                } catch (\Throwable $e) {
                    $io->writeError(sprintf('<error>Could not reach Compost at %s: %s</error>', $url, $e->getMessage()));
                    return 1;
                }
                $settings->enabled = true;
                $settings->url = $url;
                $settings->save($config);
                $io->write(sprintf('<info>Compost enabled: %s (%d mirrors)</info>', $url, count($mirrors)));
                if (strncmp($url, 'http://', 7) === 0) {
                    $io->write('<comment>Note: plain http:// needs "secure-http": false in your Composer config.</comment>');
                }
                return 0;

            case 'disable':
                if ($settings->url !== null) {
                    $discovery->forget($settings->url);
                }
                $settings->enabled = false;
                $settings->save($config);
                $io->write('<info>Compost disabled; downloads go directly upstream.</info>');
                return 0;

            case 'status':
                if (!$settings->enabled || $settings->url === null) {
                    $io->write('Compost is <comment>disabled</comment>.');
                    return 0;
                }
                $io->write(sprintf('Compost is <info>enabled</info>: %s', $settings->url));
                try {
                    foreach ($discovery->discover($settings->url, true) as $mirror) {
                        $io->write(sprintf('  %s  ->  %s%s', $mirror['upstream'], $settings->url, $mirror['path']));
                    }
                } catch (\Throwable $e) {
                    $io->writeError(sprintf('<warning>Proxy unreachable: %s</warning>', $e->getMessage()));
                    return 1;
                }
                return 0;
        }

        $io->writeError('<error>Unknown action; use enable, disable or status.</error>');
        return 1;
    }
}
