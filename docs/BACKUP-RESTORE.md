# Antimage Backup and Restore

## Backup Procedures

### Full Backup

Backs up the entire database, configuration, and CA certificates.

```bash
#!/bin/bash
# backup.sh

BACKUP_DIR="/backup/antimage/$(date +%Y%m%d_%H%M%S)"
DATA_DIR="/opt/antimage/data"
CONFIG_DIR="/opt/antimage/config"

mkdir -p "$BACKUP_DIR"

# Backup database
echo "Backing up database..."
docker-compose exec -T panel sqlite3 /data/antimage.db ".backup /data/backup.db"
docker cp antimage-panel:/data/backup.db "$BACKUP_DIR/antimage.db"

# Backup configuration
echo "Backing up configuration..."
cp -r "$CONFIG_DIR" "$BACKUP_DIR/config"

# Backup node data
echo "Backing up node data..."
cp -r "$DATA_DIR" "$BACKUP_DIR/data"

# Create tarball
cd "$(dirname "$BACKUP_DIR")"
tar -czf "$(basename "$BACKUP_DIR").tar.gz" "$(basename "$BACKUP_DIR")"
rm -rf "$BACKUP_DIR"

echo "Backup complete: $(basename "$BACKUP_DIR").tar.gz"
```

### Incremental Backup

Backs up only the database with WAL files.

```bash
#!/bin/bash
# backup-incremental.sh

BACKUP_DIR="/backup/antimage/incremental/$(date +%Y%m%d_%H%M%S)"
mkdir -p "$BACKUP_DIR"

# Backup database with WAL
docker cp antimage-panel:/data/antimage.db "$BACKUP_DIR/"
docker cp antimage-panel:/data/antimage.db-wal "$BACKUP_DIR/" 2>/dev/null || true
docker cp antimage-panel:/data/antimage.db-shm "$BACKUP_DIR/" 2>/dev/null || true

echo "Incremental backup complete: $BACKUP_DIR"
```

## Restore Procedures

### Full Restore

Restores from a full backup tarball.

```bash
#!/bin/bash
# restore.sh

if [ -z "$1" ]; then
    echo "Usage: $0 <backup-file.tar.gz>"
    exit 1
fi

BACKUP_FILE="$1"
RESTORE_DIR="/opt/antimage"

# Stop services
echo "Stopping services..."
cd "$RESTORE_DIR"
docker-compose down

# Extract backup
echo "Extracting backup..."
TEMP_DIR=$(mktemp -d)
tar -xzf "$BACKUP_FILE" -C "$TEMP_DIR"
BACKUP_CONTENT=$(ls "$TEMP_DIR")

# Restore database
echo "Restoring database..."
cp "$TEMP_DIR/$BACKUP_CONTENT/antimage.db" "$RESTORE_DIR/data/"

# Restore configuration
echo "Restoring configuration..."
cp -r "$TEMP_DIR/$BACKUP_CONTENT/config/"* "$RESTORE_DIR/config/"

# Restore node data
echo "Restoring node data..."
cp -r "$TEMP_DIR/$BACKUP_CONTENT/data/"* "$RESTORE_DIR/data/"

# Cleanup
rm -rf "$TEMP_DIR"

# Start services
echo "Starting services..."
docker-compose up -d

echo "Restore complete!"
```

## Automated Backup Schedule

Add to crontab for daily backups at 2 AM:

```bash
# Edit crontab
crontab -e

# Add this line
0 2 * * * /opt/antimage/scripts/backup.sh >> /var/log/antimage-backup.log 2>&1
```

## Disaster Recovery

### Scenario: Complete Server Loss

1. **Provision new server**
2. **Install Docker and dependencies**
3. **Run installation script**
   ```bash
   curl -fsSL https://panel.example.com/install.sh | bash
   ```
4. **Restore from backup**
   ```bash
   ./scripts/restore.sh /backup/latest.tar.gz
   ```
5. **Verify services**
   ```bash
   docker-compose ps
   docker-compose logs -f
   ```

### Scenario: Database Corruption

1. **Stop panel service**
   ```bash
   docker-compose stop panel
   ```
2. **Check database integrity**
   ```bash
   sqlite3 /opt/antimage/data/antimage.db "PRAGMA integrity_check;"
   ```
3. **If corrupted, restore from backup**
   ```bash
   cp /backup/latest/antimage.db /opt/antimage/data/
   ```
4. **Restart service**
   ```bash
   docker-compose start panel
   ```

## Backup Verification

Test backup integrity monthly:

```bash
#!/bin/bash
# verify-backup.sh

BACKUP_FILE="$1"
TEST_DIR=$(mktemp -d)

# Extract
tar -xzf "$BACKUP_FILE" -C "$TEST_DIR"

# Check database
BACKUP_CONTENT=$(ls "$TEST_DIR")
DB_FILE="$TEST_DIR/$BACKUP_CONTENT/antimage.db"

if sqlite3 "$DB_FILE" "PRAGMA integrity_check;" | grep -q "ok"; then
    echo "✓ Database integrity OK"
else
    echo "✗ Database integrity FAILED"
    rm -rf "$TEST_DIR"
    exit 1
fi

# Check configuration
if [ -f "$TEST_DIR/$BACKUP_CONTENT/config/secret.key" ]; then
    echo "✓ Secret key present"
else
    echo "✗ Secret key missing"
fi

if [ -f "$TEST_DIR/$BACKUP_CONTENT/config/ca.crt" ]; then
    echo "✓ CA certificate present"
else
    echo "✗ CA certificate missing"
fi

rm -rf "$TEST_DIR"
echo "Backup verification complete"
```

## Retention Policy

Recommended retention:

- **Daily backups**: Keep 7 days
- **Weekly backups**: Keep 4 weeks
- **Monthly backups**: Keep 12 months
- **Yearly backups**: Keep 3 years

Cleanup script:

```bash
#!/bin/bash
# cleanup-old-backups.sh

BACKUP_ROOT="/backup/antimage"

# Remove daily backups older than 7 days
find "$BACKUP_ROOT" -name "*.tar.gz" -mtime +7 -delete

# Remove incremental backups older than 1 day
find "$BACKUP_ROOT/incremental" -type d -mtime +1 -exec rm -rf {} +
```
