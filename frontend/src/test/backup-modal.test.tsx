import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, screen } from '@testing-library/react';

import BackupModal from '@/pages/index/BackupModal';
import { renderWithProviders } from './test-utils';

const realLocation = window.location;

describe('the backup dialog', () => {
  let assigned = '';
  beforeEach(() => {
    assigned = '';
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: {
        ...realLocation,
        get href() {
          return assigned;
        },
        set href(v: string) {
          assigned = v;
        },
      },
    });
  });
  afterEach(() => {
    Object.defineProperty(window, 'location', { configurable: true, value: realLocation });
    vi.restoreAllMocks();
    cleanup();
  });

  // Moving to a new server needs the certificates and keys too, so the full
  // backup is offered and asks the server for the archive.
  it('downloads the full backup with its files', () => {
    renderWithProviders(<BackupModal open basePath="/" onClose={() => {}} onBusy={() => {}} />);
    expect(screen.getByText('Full backup')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Full backup' }));
    expect(assigned).toBe('panel/api/server/getDb?full=1');
  });

  // The database alone stays available for the old file and the bots.
  it('still downloads the database alone', () => {
    renderWithProviders(<BackupModal open basePath="/" onClose={() => {}} onBusy={() => {}} />);
    fireEvent.click(screen.getByRole('button', { name: 'Back Up' }));
    expect(assigned).toBe('panel/api/server/getDb');
  });

  // A full backup is a .tar.gz, so the restore picker has to accept one.
  it('lets a full backup be picked for a restore', () => {
    const created: HTMLInputElement[] = [];
    const make = document.createElement.bind(document);
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      const el = make(tag);
      if (tag === 'input') {
        created.push(el as HTMLInputElement);
        vi.spyOn(el as HTMLInputElement, 'click').mockImplementation(() => {});
      }
      return el;
    });
    renderWithProviders(<BackupModal open basePath="/" onClose={() => {}} onBusy={() => {}} />);
    fireEvent.click(screen.getByRole('button', { name: 'Restore' }));
    expect(created.at(-1)?.accept).toContain('.tar.gz');
  });
});
