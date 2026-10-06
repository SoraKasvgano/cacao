export default {
  login: {
    username: 'Username',
    password: 'Password',
    login: 'Log in',
    register: 'Register',
    registerNow: 'register now',
    inputUsername: 'Please input your username',
    inputPassword: 'Please input your password',
    or: 'Or'
  },
  register: {
    setupToken: 'Initial setup key (optional)',
    setupTokenPlaceholder: 'Required for the first administrator',
    setupTokenHelp:
      'Only needed to create the first administrator. Ask the operator for this key. Leave blank for ordinary registration on an existing site.',
    username: 'Username',
    password: 'Password',
    register: 'Register',
    inputUsername: 'Please input your username',
    inputPassword: 'Please input your password'
  },
  loading: {
    title: 'Loading'
  },
  security: {
    requestFailed: 'Request failed. Please try again later.'
  },
  user: {
    title: 'User',
    subtitle: 'user information',
    username: 'User Name',
    changePassword: 'Change Password',
    oldPassword: 'Old Password',
    newPassword: 'New Password',
    update: 'Update',
    inputOldPassword: 'Please input old password',
    inputNewPassword: 'Please input new password'
  },
  network: {
    title: 'Network',
    subtitle: 'create and manage private networks',
    add: 'Add',
    edit: 'Edit',
    delete: 'Delete',
    netName: 'Net Name',
    password: 'Password',
    dhcp: 'DHCP',
    broadcast: 'Broadcast',
    lease: 'Lease',
    action: 'Action',
    enable: 'Enable',
    disable: 'Disable',
    modalTitle: 'Network',
    inputNetname: 'Please input network name',
    inputPassword: 'Please input password',
    inputDhcp: 'Please input DHCP configuration',
    inputLease: 'Please input lease time'
  },
  device: {
    title: 'Device',
    subtitle: 'view and manage devices',
    delete: 'Delete',
    confirmDelete: 'Are you sure delete this device?',
    yes: 'Yes',
    no: 'No',
    columns: {
      hostname: 'Host Name',
      network: 'Network',
      ip: 'IP',
      country: 'Country',
      region: 'Region',
      rx: 'RX',
      tx: 'TX',
      online: 'Online',
      os: 'OS',
      version: 'Version',
      lastActiveTime: 'Last Active At',
      action: 'Action'
    },
    status: {
      online: 'true',
      offline: 'false'
    }
  },
  route: {
    title: 'Route',
    subtitle: 'multiple local area network networking',
    add: 'Add',
    delete: 'Delete',
    modalTitle: 'Route',
    columns: {
      network: 'Network',
      devAddr: 'Device Address',
      devMask: 'Device Mask',
      dstAddr: 'Destination Address',
      dstMask: 'Destination Mask',
      nextHop: 'Next Hop',
      priority: 'Priority',
      action: 'Action'
    },
    placeholder: {
      network: 'Network',
      devAddr: 'Device Address',
      devMask: 'Device Mask',
      dstAddr: 'Destination Address',
      dstMask: 'Destination Mask',
      nextHop: 'Next Hop',
      priority: 'Priority'
    }
  },
  statistics: {
    title: 'Statistics',
    subtitle: 'user statistics',
    columns: {
      net: 'Net',
      device: 'Device',
      rx: 'RX',
      tx: 'TX'
    }
  },
  adminUser: {
    title: 'User',
    subtitle: 'user management',
    create: 'Create',
    update: 'Update',
    delete: 'Delete',
    confirmDelete: 'Are you sure delete this user?',
    yes: 'Yes',
    no: 'No',
    columns: {
      username: 'Username',
      role: 'Role',
      network: 'Network',
      device: 'Device',
      rx: 'RX',
      tx: 'TX',
      lastActiveTime: 'Last Active At',
      action: 'Action'
    },
    placeholder: {
      username: 'Username',
      password: 'Password'
    }
  },
  adminSetting: {
    title: 'Setting',
    subtitle: 'system configuration',
    register: {
      title: 'Registration Allowed',
      allowed: 'Registration Allowed',
      interval: 'Registration Interval',
      intervalUnit: 'mins'
    },
    userClean: {
      title: 'Auto Clean User',
      auto: 'Auto Clean User',
      threshold: 'Inactive User Threshold',
      thresholdUnit: 'days',
      manual: 'Manual Clean',
      clean: 'Clean',
      success: 'success'
    }
  },
  adminTasks: {
    title: 'Background tasks',
    subtitle: 'Automatic maintenance and execution status',
    retention:
      'Cleanup runs in bounded batches. Inactive users follow the existing switch and threshold; device expiry applies only to offline devices. Historical traffic is retained. Counters reset when the service restarts.',
    refresh: 'Refresh',
    autoRefresh: 'Refreshes every 5 seconds',
    name: 'Task',
    every: 'Every {seconds} seconds',
    manualOnly: 'Manual only',
    storage: 'Database write batches',
    pendingDevices: 'Devices awaiting persistence',
    storageLabels: {
      queueDepth: 'Queued operations',
      batchesCommitted: 'Committed batches',
      jobsCommitted: 'Committed operations',
      jobsFailed: 'Failed operations',
      lastBatchSize: 'Last batch size'
    },
    state: 'Status',
    running: 'Running',
    failed: 'Failed',
    ready: 'Succeeded',
    pending: 'Pending',
    lastStartedAt: 'Last started',
    lastSuccessAt: 'Last success',
    nextRunAt: 'Next run',
    duration: 'Last duration',
    rows: 'Rows: last / total',
    runs: 'Runs / failures',
    error: 'Last error',
    action: 'Action',
    run: 'Run now',
    confirmRun: 'Run this task now using its configured cleanup policy?',
    confirmInactive:
      'Clean inactive users now using the configured threshold, even if automatic user cleanup is disabled?',
    queued: 'Task scheduled',
    details: 'Last execution details',
    names: {
      maintenance: 'Business data cleanup',
      'device-flush': 'Device state persistence',
      'integrity-audit': 'Data integrity audit',
      'clean-inactive-users': 'Inactive user cleanup'
    },
    detailLabels: {
      devices: 'Devices removed',
      routes: 'Routes removed',
      nets: 'Networks removed',
      users: 'Users removed',
      expiredSessions: 'Expired sessions cleared',
      duplicateUsers: 'Duplicate username groups',
      duplicateNets: 'Duplicate network identity groups',
      duplicateDevices: 'Duplicate device identity groups',
      duplicateConfigs: 'Duplicate configuration key groups',
      duplicateDeviceIPs: 'Duplicate device IP groups',
      duplicateRoutes: 'Duplicate route groups',
      orphanNets: 'Remaining orphan networks',
      orphanDevices: 'Remaining orphan devices',
      orphanRoutes: 'Remaining orphan routes'
    }
  },
  adminLicense: {
    title: 'License',
    subtitle: 'license information',
    renew: 'Renew',
    columns: {
      licenseId: 'License ID',
      description: 'Description',
      expire: 'Expire',
      action: 'Action'
    }
  },
  components: {
    sider: {
      statistics: 'Statistics',
      network: 'Network',
      device: 'Device',
      route: 'Route',
      user: 'User',
      setting: 'Setting',
      license: 'License',
      logout: 'Logout'
    },
    footer: {
      copyright: 'Cacao © 2024'
    }
  }
}
