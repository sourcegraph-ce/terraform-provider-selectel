package selectel

import (
	"context"
	"errors"
	"fmt"
	log "github.com/sourcegraph-ce/logrus"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/helper/customdiff"
	"github.com/hashicorp/terraform-plugin-sdk/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/helper/validation"
	"github.com/selectel/go-selvpcclient/selvpcclient/resell/v2/tokens"
	v1 "github.com/selectel/mks-go/pkg/v1"
	"github.com/selectel/mks-go/pkg/v1/cluster"
)

func resourceMKSClusterV1() *schema.Resource {
	return &schema.Resource{
		Create: resourceMKSClusterV1Create,
		Read:   resourceMKSClusterV1Read,
		Update: resourceMKSClusterV1Update,
		Delete: resourceMKSClusterV1Delete,
		Importer: &schema.ResourceImporter{
			State: resourceMKSClusterV1ImportState,
		},
		CustomizeDiff: customdiff.All(
			customdiff.ComputedIf(
				"maintenance_window_end",
				func(d *schema.ResourceDiff, meta interface{}) bool {
					return d.HasChange("maintenance_window_start")
				}),
		),
		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(60 * time.Minute),
			Update: schema.DefaultTimeout(60 * time.Minute),
			Delete: schema.DefaultTimeout(60 * time.Minute),
		},
		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
				DiffSuppressFunc: func(k, old, new string, d *schema.ResourceData) bool {
					return strings.EqualFold(old, new)
				},
			},
			"project_id": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"region": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
				ValidateFunc: validation.StringInSlice([]string{
					ru1Region,
					ru2Region,
					ru3Region,
					ru7Region,
					ru8Region,
				}, false),
			},
			"kube_version": {
				Type:             schema.TypeString,
				Required:         true,
				ForceNew:         false,
				DiffSuppressFunc: mksClusterV1KubeVersionDiffSuppressFunc,
				StateFunc: func(v interface{}) string {
					return strings.TrimPrefix(v.(string), "v")
				},
			},
			"enable_autorepair": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  true,
				ForceNew: false,
			},
			"enable_patch_version_auto_upgrade": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  true,
				ForceNew: false,
			},
			"network_id": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ForceNew: true,
			},
			"subnet_id": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ForceNew: true,
			},
			"maintenance_window_start": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ForceNew: false,
			},
			"maintenance_window_end": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"kube_api_ip": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"status": {
				Type:     schema.TypeString,
				Computed: true,
			},
		},
	}
}

func resourceMKSClusterV1Create(d *schema.ResourceData, meta interface{}) error {
	config := meta.(*Config)
	resellV2Client := config.resellV2Client()
	ctx := context.Background()
	tokenOpts := tokens.TokenOpts{
		ProjectID: d.Get("project_id").(string),
	}

	log.Print(msgCreate(objectToken, tokenOpts))
	token, _, err := tokens.Create(ctx, resellV2Client, tokenOpts)
	if err != nil {
		return errCreatingObject(objectToken, err)
	}

	region := d.Get("region").(string)
	endpoint := getMKSClusterV1Endpoint(region)
	mksClient := v1.NewMKSClientV1(token.ID, endpoint)

	// Prepare cluster create options.
	enableAutorepair := d.Get("enable_autorepair").(bool)
	enablePatchVersionAutoUpgrade := d.Get("enable_patch_version_auto_upgrade").(bool)
	createOpts := &cluster.CreateOpts{
		Name:                          d.Get("name").(string),
		NetworkID:                     d.Get("network_id").(string),
		SubnetID:                      d.Get("subnet_id").(string),
		KubeVersion:                   d.Get("kube_version").(string),
		MaintenanceWindowStart:        d.Get("maintenance_window_start").(string),
		EnableAutorepair:              &enableAutorepair,
		EnablePatchVersionAutoUpgrade: &enablePatchVersionAutoUpgrade,
		Region:                        region,
	}

	log.Print(msgCreate(objectCluster, createOpts))
	newCluster, _, err := cluster.Create(ctx, mksClient, createOpts)
	if err != nil {
		return errCreatingObject(objectCluster, err)
	}

	log.Printf("[DEBUG] waiting for cluster %s to become 'ACTIVE'", newCluster.ID)
	timeout := d.Timeout(schema.TimeoutCreate)
	err = waitForMKSClusterV1ActiveState(ctx, mksClient, newCluster.ID, timeout)
	if err != nil {
		return errCreatingObject(objectCluster, err)
	}

	d.SetId(newCluster.ID)

	return resourceMKSClusterV1Read(d, meta)
}

func resourceMKSClusterV1Read(d *schema.ResourceData, meta interface{}) error {
	config := meta.(*Config)
	resellV2Client := config.resellV2Client()
	ctx := context.Background()
	tokenOpts := tokens.TokenOpts{
		ProjectID: d.Get("project_id").(string),
	}

	log.Print(msgCreate(objectToken, tokenOpts))
	token, _, err := tokens.Create(ctx, resellV2Client, tokenOpts)
	if err != nil {
		return errCreatingObject(objectToken, err)
	}

	region := d.Get("region").(string)
	endpoint := getMKSClusterV1Endpoint(region)
	mksClient := v1.NewMKSClientV1(token.ID, endpoint)

	log.Print(msgGet(objectCluster, d.Id()))
	mksCluster, response, err := cluster.Get(ctx, mksClient, d.Id())
	if err != nil {
		if response != nil {
			if response.StatusCode == http.StatusNotFound {
				d.SetId("")
				return nil
			}
		}

		return errGettingObject(objectCluster, d.Id(), err)
	}

	d.Set("name", mksCluster.Name)
	d.Set("status", mksCluster.Status)
	d.Set("project_id", mksCluster.ProjectID)
	d.Set("network_id", mksCluster.NetworkID)
	d.Set("subnet_id", mksCluster.SubnetID)
	d.Set("kube_api_ip", mksCluster.KubeAPIIP)
	d.Set("kube_version", mksCluster.KubeVersion)
	d.Set("region", mksCluster.Region)
	d.Set("maintenance_window_start", mksCluster.MaintenanceWindowStart)
	d.Set("maintenance_window_end", mksCluster.MaintenanceWindowEnd)
	d.Set("enable_autorepair", mksCluster.EnableAutorepair)
	d.Set("enable_patch_version_auto_upgrade", mksCluster.EnablePatchVersionAutoUpgrade)

	return nil
}

func resourceMKSClusterV1Update(d *schema.ResourceData, meta interface{}) error {
	config := meta.(*Config)
	resellV2Client := config.resellV2Client()
	ctx := context.Background()
	tokenOpts := tokens.TokenOpts{
		ProjectID: d.Get("project_id").(string),
	}

	log.Print(msgCreate(objectToken, tokenOpts))
	token, _, err := tokens.Create(ctx, resellV2Client, tokenOpts)
	if err != nil {
		return errCreatingObject(objectToken, err)
	}

	region := d.Get("region").(string)
	endpoint := getMKSClusterV1Endpoint(region)
	mksClient := v1.NewMKSClientV1(token.ID, endpoint)

	if d.HasChange("kube_version") {
		if err := upgradeMKSClusterV1KubeVersion(ctx, d, mksClient); err != nil {
			return errUpdatingObject(objectCluster, d.Id(), err)
		}
	}

	var updateOpts cluster.UpdateOpts
	if d.HasChange("maintenance_window_start") {
		updateOpts.MaintenanceWindowStart = d.Get("maintenance_window_start").(string)
	}
	if d.HasChange("enable_autorepair") {
		v := d.Get("enable_autorepair").(bool)
		updateOpts.EnableAutorepair = &v
	}
	if d.HasChange("enable_patch_version_auto_upgrade") {
		v := d.Get("enable_patch_version_auto_upgrade").(bool)
		updateOpts.EnablePatchVersionAutoUpgrade = &v
	}

	if updateOpts != (cluster.UpdateOpts{}) {
		log.Print(msgUpdate(objectCluster, d.Id(), updateOpts))
		_, _, err := cluster.Update(ctx, mksClient, d.Id(), &updateOpts)
		if err != nil {
			return errUpdatingObject(objectCluster, d.Id(), err)
		}

		log.Printf("[DEBUG] waiting for cluster %s to become 'ACTIVE'", d.Id())
		timeout := d.Timeout(schema.TimeoutUpdate)
		err = waitForMKSClusterV1ActiveState(ctx, mksClient, d.Id(), timeout)
		if err != nil {
			return errUpdatingObject(objectCluster, d.Id(), err)
		}
	}

	return resourceMKSClusterV1Read(d, meta)
}

func resourceMKSClusterV1Delete(d *schema.ResourceData, meta interface{}) error {
	config := meta.(*Config)
	resellV2Client := config.resellV2Client()
	ctx := context.Background()
	tokenOpts := tokens.TokenOpts{
		ProjectID: d.Get("project_id").(string),
	}

	log.Print(msgCreate(objectToken, tokenOpts))
	token, _, err := tokens.Create(ctx, resellV2Client, tokenOpts)
	if err != nil {
		return errCreatingObject(objectToken, err)
	}

	region := d.Get("region").(string)
	endpoint := getMKSClusterV1Endpoint(region)
	mksClient := v1.NewMKSClientV1(token.ID, endpoint)

	log.Print(msgDelete(objectCluster, d.Id()))
	_, err = cluster.Delete(ctx, mksClient, d.Id())
	if err != nil {
		return errDeletingObject(objectCluster, d.Id(), err)
	}

	stateConf := &resource.StateChangeConf{
		Pending: []string{strconv.Itoa(http.StatusOK)},
		Target:  []string{strconv.Itoa(http.StatusNotFound)},
		Refresh: func() (result interface{}, state string, err error) {
			result, response, err := cluster.Get(ctx, mksClient, d.Id())
			if err != nil {
				if response != nil {
					return result, strconv.Itoa(response.StatusCode), nil
				}

				return nil, "", err
			}

			return result, strconv.Itoa(response.StatusCode), err
		},
		Timeout:    d.Timeout(schema.TimeoutDelete),
		Delay:      10 * time.Second,
		MinTimeout: 3 * time.Second,
	}

	log.Printf("[DEBUG] waiting for cluster %s to become deleted", d.Id())
	_, err = stateConf.WaitForState()
	if err != nil {
		return fmt.Errorf("error waiting for the cluster %s to become deleted: %s", d.Id(), err)
	}

	return nil
}

func resourceMKSClusterV1ImportState(d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	config := meta.(*Config)
	if config.ProjectID == "" {
		return nil, errors.New("SEL_PROJECT_ID must be set for the resource import")
	}
	if config.Region == "" {
		return nil, errors.New("SEL_REGION must be set for the resource import")
	}

	d.Set("project_id", config.ProjectID)
	d.Set("region", config.Region)

	return []*schema.ResourceData{d}, nil
}
